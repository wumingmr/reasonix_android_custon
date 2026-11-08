package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/provider"
	"reasonix/internal/tool/builtin"
	"reasonix/internal/workspacelease"
)

func TestWorkspaceConflictReturnsActionableToolResult(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	holder, err := workspacelease.New(root, locks, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := workspacelease.New(root, locks, func() { t.Error("unexpected automatic wait notice") })
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "held.go")
	release, err := holder.HoldWriteForPath(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	writer := &recordingWriter{name: "write_file"}
	a := deliveryLeaseTestAgent(t, owner, writer)
	a.writeWorkspaceRoot = root
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out := a.executeOne(ctx, &a.turn, provider.ToolCall{ID: "w", Name: writer.Name(),
		Arguments: string(mustJSON(t, map[string]string{"path": path, "content": "no"}))})
	if !out.blocked || ctx.Err() != nil {
		t.Fatalf("not an immediate conflict: %+v, context=%v", out, ctx.Err())
	}
	for _, want := range []string{"WRITE_CONFLICT", path, "NOT executed", "different file", "re-read", "No automatic wait"} {
		if !strings.Contains(out.output, want) {
			t.Errorf("missing %q in %s", want, out.output)
		}
	}
	other := filepath.Join(root, "other.go")
	// Avoid a rare conservative stripe collision in this parallelism fixture.
	for {
		probe, err := owner.TryHoldWriteForPaths(ctx, []string{other})
		if err == nil {
			probe()
			break
		}
		var conflict *workspacelease.ConflictError
		if !errors.As(err, &conflict) || conflict.Scope != "file" || ctx.Err() != nil {
			t.Fatalf("different-file fixture: %v", err)
		}
		other += "x"
	}
	out = a.executeOne(ctx, &a.turn, provider.ToolCall{ID: "other", Name: writer.Name(),
		Arguments: string(mustJSON(t, map[string]string{"path": other, "content": "yes"}))})
	if out.blocked || out.errMsg != "" {
		t.Fatalf("same directory different file blocked: %+v", out)
	}
}

func TestWorkspaceConflictOpaqueAndHooksRecovery(t *testing.T) {
	for _, hooks := range []bool{false, true} {
		plan := &toolCallPlan{runTool: &workspaceLeaseTestTool{name: "bash"}, hooksMayMutateWorkspace: hooks}
		out := workspaceWriteBlockedOutcome(plan, &workspacelease.ConflictError{Scope: "workspace", Target: "/test", Reason: "busy"})
		wants := []string{"does NOT prove", "NOT executed", "No automatic wait"}
		if hooks {
			wants = append(wants, "Configured tool hooks", "Changing only the tool's path cannot")
		} else {
			wants = append(wants, "additional_write_dirs", "edit_file/write_file/move_file", "Do not blindly retry")
		}
		for _, want := range wants {
			if !strings.Contains(out.output, want) {
				t.Errorf("missing %q in %s", want, out.output)
			}
		}
	}
	out := workspaceWriteBlockedOutcome(&toolCallPlan{runTool: &workspaceLeaseTestTool{name: "bash"}},
		&workspacelease.ConflictError{Scope: "session", Target: "/test", Reason: "active file hold"})
	for _, want := range []string{"own write coordination", "not proof that another session", "finish/cancel"} {
		if !strings.Contains(out.output, want) {
			t.Errorf("missing %q in %s", want, out.output)
		}
	}
}

func TestStaticShellWriteUsesFileLease(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	holder, err := workspacelease.New(root, locks, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := workspacelease.New(root, locks, nil)
	if err != nil {
		t.Fatal(err)
	}
	release, err := holder.HoldWriteForPath(context.Background(), filepath.Join(root, "held.go"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	a := deliveryLeaseTestAgent(t, owner, (builtin.Workspace{Dir: root}).Tools("bash")[0])
	a.writeWorkspaceRoot = root
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out := a.executeOne(ctx, &a.turn, provider.ToolCall{ID: "shell", Name: "bash", Arguments: `{"command":"printf ok > held.go"}`})
	if !out.blocked || !strings.Contains(out.output, "file write conflict") || ctx.Err() != nil {
		t.Fatalf("static shell was not file-scoped: %+v", out)
	}
	out = a.executeOne(ctx, &a.turn, provider.ToolCall{ID: "opaque", Name: "bash", Arguments: `{"command":"python -c 'print(1)'"}`})
	if !out.blocked || !strings.Contains(out.output, "workspace write conflict") || !strings.Contains(out.output, "additional_write_dirs") {
		t.Fatalf("opaque shell did not retain workspace protection: %+v", out)
	}
}
