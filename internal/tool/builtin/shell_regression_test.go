package builtin

import (
	"context"
	"encoding/json"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
)

func TestShellTimeoutClampsBeforeDurationConversion(t *testing.T) {
	for _, cap := range []time.Duration{time.Second, 500 * time.Microsecond, 0} {
		b := bash{timeout: cap}
		for _, ms := range []int{1, 1500, 9223372036855, int(^uint(0) >> 1)} {
			got := b.foregroundTimeoutFor(bashParams{TimeoutMS: ms})
			if got <= 0 || (cap > 0 && got > cap) {
				t.Fatalf("cap=%v ms=%d got=%v", cap, ms, got)
			}
		}
	}
	if got := cappedMilliseconds(9223372036855, jobOutputMaxWait); got != jobOutputMaxWait {
		t.Fatalf("job wait overflow: %v", got)
	}
}

func TestInvalidJobFilterPreservesUnreadOutput(t *testing.T) {
	for _, reader := range []tool.Tool{jobOutput{}, bashOutput{}} {
		t.Run(reader.Name(), func(t *testing.T) {
			m := jobs.NewManager(event.Discard)
			defer m.Close()
			ctx := jobs.WithManager(t.Context(), m)
			j := m.Start("pwsh", "failure", func(_ context.Context, w io.Writer) (string, error) {
				_, _ = io.WriteString(w, "startup diagnostic to preserve\n")
				return "", nil
			})
			m.Wait(ctx, []string{j.ID}, 5)
			args, _ := json.Marshal(map[string]any{"job_id": j.ID, "filter": "["})
			if _, err := reader.Execute(ctx, args); err == nil {
				t.Fatal("invalid filter accepted")
			}
			args, _ = json.Marshal(map[string]any{"job_id": j.ID})
			got, err := reader.Execute(ctx, args)
			if err != nil || !strings.Contains(got, "startup diagnostic to preserve") {
				t.Fatalf("lost log: %q, %v", got, err)
			}
		})
	}
}

func TestBackgroundPermissionFailureOffersExactRetry(t *testing.T) {
	if !sandbox.OSSandboxSupported() {
		t.Skip("exact denial retries require an OS shell sandbox")
	}
	previous := bashSandboxCommand
	bashSandboxCommand = func(_ sandbox.Spec, _ sandbox.Shell, _ string) ([]string, bool) {
		if runtime.GOOS == "windows" {
			return []string{"cmd", "/c", "echo Error: spawn EPERM: operation not permitted & exit /b 1"}, true
		}
		return []string{"sh", "-c", "printf 'Error: spawn EPERM: operation not permitted\\n'; exit 1"}, true
	}
	t.Cleanup(func() { bashSandboxCommand = previous })
	m := jobs.NewManager(event.Discard)
	defer m.Close()
	ctx := sandbox.WithPermissionPreset(jobs.WithManager(t.Context(), m), "workspace-write")
	b := bash{name: "pwsh", shell: sandbox.Shell{Kind: sandbox.ShellPowerShell, Path: "pwsh"}, workDir: t.TempDir(), sb: sandbox.Spec{Mode: "enforce", Network: true}}
	_, err := b.Execute(ctx, json.RawMessage(`{"command":"npm run start","description":"Start service","run_in_background":true}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := (jobOutput{}).ExecuteDetailed(ctx, json.RawMessage(`{"job_id":"pwsh-1","wait":true,"timeout_ms":5000}`))
	if err != nil || !strings.Contains(got.Output, "denial_id ") {
		t.Fatalf("no authorized retry path: %q, %v", got.Output, err)
	}
	if got.Execution.FailurePhase != tool.ShellPhaseExecution || got.Execution.MutationRisk != tool.ShellMutationMayBePartial {
		t.Fatalf("runtime error reclassified: %+v", got.Execution)
	}
	id := strings.TrimSuffix(strings.Fields(strings.SplitN(got.Output, "denial_id ", 2)[1])[0], ".")
	if !sandbox.ConsumeDenial(id, "npm run start") || sandbox.ConsumeDenial(id, "npm run start") {
		t.Fatal("denial must authorize one exact retry")
	}
}

func TestBackgroundDiagnosticCaptureKeepsFailureTail(t *testing.T) {
	w := &boundedDiagnosticWriter{limit: 64 << 10}
	_, _ = io.WriteString(w, strings.Repeat("ordinary output\n", 10_000))
	_, _ = io.WriteString(w, "Error: spawn EPERM: operation not permitted\n")
	if !strings.Contains(w.String(), "spawn EPERM") || len(w.String()) > 64<<10 {
		t.Fatalf("bounded diagnostic lost failure tail: bytes=%d", len(w.String()))
	}
}
