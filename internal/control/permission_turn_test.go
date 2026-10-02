package control

import (
	"context"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

type presetTurnRunner struct {
	session *agent.Session
	release chan struct{}
}

func (r presetTurnRunner) Run(ctx context.Context, input string) error {
	r.session.Add(provider.Message{Role: provider.RoleUser, Content: input})
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestPermissionPresetChangeCancelsRunningTurnOnlyWhenNotWidening(t *testing.T) {
	cases := []struct {
		name       string
		from, to   string
		wantCancel bool
	}{
		{"workspace to full access", ToolApprovalWorkspaceWrite, ToolApprovalDangerFullAccess, false},
		{"read-only to workspace", ToolApprovalReadOnly, ToolApprovalWorkspaceWrite, false},
		{"full access to workspace", ToolApprovalDangerFullAccess, ToolApprovalWorkspaceWrite, true},
		{"workspace to read-only", ToolApprovalWorkspaceWrite, ToolApprovalReadOnly, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			c := newOwnedTestController(t, Options{Runner: presetTurnRunner{session: agent.NewSession("sys"), release: release}})
			c.SetToolApprovalMode(tc.from)
			done := make(chan error, 1)
			go func() { done <- c.RunTurn(context.Background(), "work") }()
			waitForRunning(t, c)

			c.SetToolApprovalMode(tc.to)
			if got := c.ToolApprovalMode(); got != tc.to {
				t.Fatalf("mode = %q, want %q", got, tc.to)
			}
			if tc.wantCancel {
				waitIdle(t, c)
			} else {
				select {
				case err := <-done:
					t.Fatalf("widening the preset ended the running turn: %v", err)
				case <-time.After(200 * time.Millisecond):
				}
				if !c.Running() {
					t.Fatal("widening the preset stopped the running turn")
				}
			}
			close(release)
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("turn did not finish")
			}
		})
	}
}
