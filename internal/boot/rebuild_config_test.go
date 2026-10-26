package boot

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/sessioninbox"
)

func TestRebuildFromCompactRatioRefreshesActiveSession(t *testing.T) {
	isolateConfigHome(t)
	root := robustTempDir(t)
	t.Chdir(root)
	writeRuntimeFixture(t, root)
	writeCompactRatio(t, config.UserConfigPath(), .85)
	old, err := BuildRuntime(t.Context(), withTestSession(t, Options{WorkspaceRoot: root}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(old.Controller.Close)
	ctrl := old.Controller
	ctrl.EnsureSessionPath()
	service, runtime, bound := ctrl.SessionBinding()
	ref, ok := ctrl.SessionRef()
	if !ok || !bound {
		t.Fatal("missing active session identity")
	}
	ctrl.AdoptHistory([]provider.Message{
		{Role: provider.RoleSystem, Content: systemMessage(ctrl.History())},
		{Role: provider.RoleUser, Content: "keep my existing conversation"},
		{Role: provider.RoleAssistant, Content: "existing response"},
	}, "")
	ctrl.SetToolApprovalMode(control.ToolApprovalYolo)
	ctrl.SetPlanMode(true)
	ctrl.SetGoal("finish this conversation")
	ctrl.RestoreSessionAuthorizations(control.SessionAuthorizations{
		Grants: []string{"bash(go test ./...)"}, PlanModeReadOnlyCommands: []string{"git status"},
	})
	inboxID := queueRebuildInbox(t, ctrl)
	history, authorizations := ctrl.History(), ctrl.SessionAuthorizations()
	if got := ctrl.CompactRatio(); got != .85 {
		t.Fatalf("initial compact ratio = %v, want .85", got)
	}

	writeCompactRatio(t, config.UserConfigPath(), .80)
	next, err := RebuildFrom(t.Context(), old, Options{WorkspaceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.Controller.Close)
	if got := next.Controller.CompactRatio(); got != .80 {
		t.Fatalf("live compact ratio = %v, want saved .80", got)
	}
	if !next.ReusedController || next.Controller != ctrl {
		t.Fatal("compact ratio replaced the active controller")
	}
	if got, ok := next.Controller.SessionRef(); !ok || got != ref {
		t.Fatalf("session identity = %+v, want %+v", got, ref)
	}
	if gotService, gotRuntime, ok := next.Controller.SessionBinding(); !ok || gotService != service || gotRuntime != runtime {
		t.Fatal("compact ratio rebuild replaced the session runtime or its service")
	}
	if !reflect.DeepEqual(next.Controller.History(), history) {
		t.Fatal("compact ratio rebuild changed conversation history")
	}
	if !reflect.DeepEqual(next.Controller.SessionAuthorizations(), authorizations) ||
		next.Controller.ToolApprovalMode() != control.ToolApprovalYolo || !next.Controller.PlanMode() ||
		next.Controller.Goal() != ctrl.Goal() || next.Controller.SessionTemp() != ctrl.SessionTemp() {
		t.Fatal("compact ratio rebuild lost session-owned state")
	}
	if next.Snapshot.CacheHash() != old.Snapshot.CacheHash() || next.Plan.PrefixChanged ||
		!reflect.DeepEqual(next.Controller.ToolContractEntries(), ctrl.ToolContractEntries()) {
		t.Fatal("compact ratio rebuild changed the provider-visible cache prefix")
	}
	assertRebuildInbox(t, next.Controller, inboxID)
	if err := next.Controller.Snapshot(); err != nil {
		t.Fatalf("live update closed the session writer: %v", err)
	}
	unchanged, err := RebuildFrom(t.Context(), next, Options{WorkspaceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if !unchanged.ReusedController || unchanged.Controller != next.Controller {
		t.Fatal("unchanged effective configuration no longer reuses the controller")
	}
	assertRebuildInbox(t, unchanged.Controller, inboxID)
}

func TestRebuildFromCompactRatioHonorsConfigSnapshot(t *testing.T) {
	isolateConfigHome(t)
	root := robustTempDir(t)
	t.Chdir(root)
	writeRuntimeFixture(t, root)
	writeCompactRatio(t, config.UserConfigPath(), .85)
	cfg, err := config.LoadModelRuntimeSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	old, err := BuildRuntime(t.Context(), Options{WorkspaceRoot: root, ConfigSnapshot: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(old.Controller.Close)
	snapshot := *cfg
	snapshot.Agent.CompactRatio = .80
	next, err := RebuildFrom(t.Context(), old, Options{WorkspaceRoot: root, ConfigSnapshot: &snapshot})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.Controller.Close)
	if got := next.Controller.CompactRatio(); got != .80 {
		t.Fatalf("snapshot compact ratio = %v, want .80", got)
	}
	if got := config.LoadForEdit(config.UserConfigPath()).Agent.CompactRatio; got != .85 {
		t.Fatalf("snapshot rebuild changed persisted ratio to %v", got)
	}
}

func TestRebuildFromCompactRatioPreservesProjectOverride(t *testing.T) {
	isolateConfigHome(t)
	root := robustTempDir(t)
	t.Chdir(root)
	writeRuntimeFixture(t, root)
	writeCompactRatio(t, config.UserConfigPath(), .85)
	writeCompactRatio(t, filepath.Join(root, "reasonix.toml"), .70)
	old, err := BuildRuntime(t.Context(), Options{WorkspaceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(old.Controller.Close)
	if got := old.Controller.CompactRatio(); got != .70 {
		t.Fatalf("initial effective compact ratio = %v, want .70", got)
	}
	writeCompactRatio(t, config.UserConfigPath(), .80)
	next, err := RebuildFrom(t.Context(), old, Options{WorkspaceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.Controller.Close)
	if got := next.Controller.CompactRatio(); got != .70 {
		t.Fatalf("effective compact ratio = %v, want project override .70", got)
	}
	if !next.ReusedController || next.Controller != old.Controller {
		t.Fatal("unchanged project-effective configuration rebuilt the controller")
	}
}

func TestRebuildFromCompactRatioFailureKeepsController(t *testing.T) {
	isolateConfigHome(t)
	root := robustTempDir(t)
	t.Chdir(root)
	writeRuntimeFixture(t, root)
	writeCompactRatio(t, config.UserConfigPath(), .85)
	old, err := BuildRuntime(t.Context(), withTestSession(t, Options{WorkspaceRoot: root}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(old.Controller.Close)
	old.Controller.EnsureSessionPath()
	ref, _ := old.Controller.SessionRef()
	history := old.Controller.History()
	generation := old.Snapshot.Generation()
	writeCompactRatio(t, config.UserConfigPath(), .80)
	next, err := RebuildFrom(t.Context(), old, Options{WorkspaceRoot: root, Model: "missing-model"})
	if !errors.Is(err, ErrUnknownModel) || next != nil {
		t.Fatalf("failed rebuild = %v, %v; want no replacement and unknown model", next, err)
	}
	if got, ok := old.Controller.SessionRef(); !ok || got != ref ||
		!reflect.DeepEqual(old.Controller.History(), history) || old.Controller.CompactRatio() != .85 ||
		old.Runtime.Closed() || !old.Owner.Gate.AdmitNewWork(generation) {
		t.Fatal("failed configuration rebuild changed or retired the live session")
	}
	writeCompactRatio(t, config.UserConfigPath(), .85)
	restored, err := RebuildFrom(t.Context(), old, Options{WorkspaceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if !restored.ReusedController || restored.Controller != old.Controller {
		t.Fatal("failed rebuild replaced the previous configuration snapshot")
	}
}

func writeCompactRatio(t *testing.T, path string, ratio float64) {
	t.Helper()
	cfg := config.LoadForEdit(path)
	if err := cfg.SetCompactRatio(ratio); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
}

func queueRebuildInbox(t *testing.T, ctrl *control.Controller) string {
	t.Helper()
	if err := ctrl.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	receipt, err := ctrl.EnqueueInbox(control.InboxRequest{
		Submit: "continue after rebuilding", Source: "test", Idempotency: "compact-ratio-rebuild",
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt.ItemID
}

func assertRebuildInbox(t *testing.T, ctrl *control.Controller, id string) {
	t.Helper()
	snapshot := ctrl.InboxSnapshot()
	if !snapshot.Paused || len(snapshot.Items) != 1 || snapshot.Items[0].ID != id || snapshot.Items[0].State != sessioninbox.StateQueued {
		t.Fatalf("rebuild lost queued inbox state: %+v", snapshot)
	}
	_, envelope, err := ctrl.ReadInboxItem(id)
	if err != nil || envelope.SubmitText != "continue after rebuilding" {
		t.Fatalf("rebuild lost durable inbox body: %+v, %v", envelope, err)
	}
}
