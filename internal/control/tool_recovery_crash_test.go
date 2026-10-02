package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

type crashAfterEffectTool struct{ path string }

func (crashAfterEffectTool) Name() string            { return "crash_after_effect" }
func (crashAfterEffectTool) Description() string     { return "test fixture" }
func (crashAfterEffectTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (crashAfterEffectTool) ReadOnly() bool          { return false }
func (t crashAfterEffectTool) Execute(context.Context, json.RawMessage) (string, error) {
	f, err := os.OpenFile(t.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return "", err
	}
	if _, err = f.WriteString("effect\n"); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	_ = f.Close()
	os.Exit(73) // A committed external effect, with no local result receipt.
	return "", nil
}

func TestToolRecoveryCrashAfterEffect(t *testing.T) {
	if root := os.Getenv("REASONIX_RECOVERY_CRASH_FIXTURE"); root != "" {
		reg := tool.NewRegistry()
		reg.Add(crashAfterEffectTool{path: filepath.Join(root, "effects")})
		p := &recordingProvider{streams: [][]provider.Chunk{{{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "crash", Name: "crash_after_effect", Arguments: `{}`}}, {Type: provider.ChunkDone}}}}
		a := agent.New(p, reg, agent.NewSession("sys"), agent.Options{}, event.Discard)
		c := newOwnedTestController(t, Options{Executor: a, Runner: a, SessionPath: filepath.Join(root, "session.jsonl"), SessionDir: root, Sink: event.Discard})
		if err := c.RunTurn(context.Background(), "perform effect"); err != nil {
			t.Fatal(err)
		}
		t.Fatal("fixture failed to crash")
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestToolRecoveryCrashAfterEffect$")
	cmd.Env = append(os.Environ(), "REASONIX_RECOVERY_CRASH_FIXTURE="+root)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("crash helper: %v %s", err, out)
	}
	path := filepath.Join(root, "session.jsonl")
	a := agent.New(nil, tool.NewRegistry(), agent.NewSession("sys"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: a, SessionPath: path, SessionDir: root, Sink: event.Discard})
	defer c.Close()
	c.recoverInterruptedTurn(path)
	view := c.ToolRecoverySnapshot()
	if !view.Retired || view.RetryEnabled || len(view.Calls) != 0 {
		t.Fatalf("unresolved crash effects=%+v", view)
	}
	req := ToolRecoveryRequest{SessionPath: view.SessionPath, RuntimeEpoch: view.RuntimeEpoch, Revision: view.Revision, AttemptID: "crash", Action: "inspect"}
	if _, err = c.ResolveToolRecovery(context.Background(), req); err == nil || !strings.Contains(err.Error(), "tool_recovery_retired") {
		t.Fatalf("retired recovery action err=%v", err)
	}
	projection := loadDurableSessionProjection(t, path)
	if len(projection.ActiveTools) != 0 || projection.TurnStatus != event.TurnInterrupted {
		t.Fatal("retired endpoint rewrote the historical unknown fact")
	}
	commits, err := session.Replay(sessionDirectory(path), nil)
	if err != nil {
		t.Fatal(err)
	}
	unknown := false
	for _, commit := range commits {
		for _, recorded := range commit.Events {
			if recorded.Kind != "tool/result" {
				continue
			}
			var body struct {
				ID    string `json:"id"`
				State string `json:"state"`
			}
			if json.Unmarshal(recorded.Payload, &body) == nil && body.ID == "crash" && body.State == "result_unknown" {
				unknown = true
			}
		}
	}
	if !unknown {
		t.Fatal("restart did not preserve the unknown external result as a typed v3 fact")
	}
	effects, err := os.ReadFile(filepath.Join(root, "effects"))
	if err != nil || string(effects) != "effect\n" {
		t.Fatalf("effect repeated: %q %v", effects, err)
	}
}
