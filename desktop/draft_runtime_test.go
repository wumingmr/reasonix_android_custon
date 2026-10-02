package main

import (
	"encoding/json"
	"path/filepath"
	"reasonix/desktop/internal/draftstate"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"testing"
)

func TestDraftRetryRebuildsCanonicalControllerAndPreservesFailedCandidate(t *testing.T) {
	a, tab, old, _ := auditMigratedTab(t)
	cfg, err := config.LoadForRootReadOnly(tab.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == "new" {
			cfg.Providers[i].SupportedEfforts = []string{"low", "high"}
			cfg.Providers[i].DefaultEffort = "low"
		}
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}
	a.desktopDrafts = draftstate.New(filepath.Join(t.TempDir(), "drafts.sqlite"))
	t.Cleanup(func() { _ = a.desktopDrafts.Close(); tab.Ctrl.Close() })
	_, op := beginDraftTestOperation(t, a, "starting")
	// Bind the fixture operation to the already canonical session, as a retry
	// does after the first Controller has become ready but admission failed.
	op.SessionID = tab.SessionID
	identity := tab.SessionID
	settings := SessionDraftSettings{Model: "new/new-model", Effort: "high", ToolApprovalMode: "read-only", CollaborationMode: "plan", DisabledMCP: map[string]ServerView{"unused": {Name: "unused"}}, MCPOrder: []string{"unused"}}
	if err := a.prepareDraftRuntime(op, settings); err != nil {
		t.Fatal(err)
	}
	if tab.Ctrl == old || tab.SessionID != identity {
		t.Fatal("retry did not replace the graph on the same Session")
	}
	if err := a.verifyDraftRuntime(tab.ID, settings); err != nil {
		t.Fatal(err)
	}
	current := tab.Ctrl
	settings.Model = "removed/model"
	if err := a.prepareDraftRuntime(op, settings); err == nil {
		t.Fatal("missing model accepted")
	}
	if tab.Ctrl != current || tab.SessionID != identity {
		t.Fatal("failed candidate replaced the execution owner")
	}
}

func TestDraftCancelWaitsForWorkerOwnership(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := newDraftTestApp(t)
	_, op := beginDraftTestOperation(t, a, "starting")
	release, err := a.draftStore().WorkerLease(op.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	view, err := a.CancelDraftSubmission(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Phase != "cancel_requested" || view.CanEdit || view.CanResume {
		t.Fatalf("cancel unlocked live worker: %+v", view)
	}
	if unlock, err := a.lockDraftRuntimePublication(op.ID); err == nil {
		unlock()
		t.Fatal("cancelled worker can publish a runtime")
	}
	a.finishDraftCancellation(op)
	view, err = a.GetDraftSubmission(op.ID)
	if err != nil || view.Phase != "cancelled" {
		t.Fatalf("worker finish: %+v %v", view, err)
	}
}

func TestDraftV3SnapshotSurvivesV5DatabaseAndChangedEditor(t *testing.T) {
	a := newDraftTestApp(t)
	draft, op := beginDraftTestOperation(t, a, "reserved")
	request := SessionDraftSubmissionRequest{SnapshotVersion: 3, Settings: SessionDraftSettings{Model: "frozen/model", ToolApprovalMode: "read-only"}}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	op.RequestJSON = string(encoded)
	settings, err := a.draftOperationSettings(op)
	if err != nil || settings.Model != "frozen/model" {
		t.Fatalf("frozen settings: %+v %v (draft %s)", settings, err, draft.ID)
	}
	request.SnapshotVersion = draftstate.SnapshotVersion + 1
	encoded, _ = json.Marshal(request)
	op.RequestJSON = string(encoded)
	if _, err = a.draftOperationSettings(op); err == nil {
		t.Fatal("future snapshot was interpreted as current editor settings")
	}
}

func TestDraftV5InheritedModelSnapshotRemainsFrozenForRetry(t *testing.T) {
	a := newDraftTestApp(t)
	_, op := beginDraftTestOperation(t, a, "reserved")
	request := SessionDraftSubmissionRequest{
		SnapshotVersion: draftstate.SnapshotVersion,
		Settings: SessionDraftSettings{
			Model: "fixture/frozen", ModelSource: draftModelSourceDefault, ToolApprovalMode: "read-only",
		},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	op.RequestJSON = string(encoded)
	settings, err := a.draftOperationSettings(op)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Model != "fixture/frozen" || settings.ModelSource != draftModelSourceDefault {
		t.Fatalf("retry settings = %+v, want the submission-time effective model", settings)
	}
}

func TestDraftRetryChangesActualReadyControllerPermission(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := newDraftTestApp(t)
	_, op := beginDraftTestOperation(t, a, "starting")
	ctrl := newFixtureController(t, control.Options{ModelRef: "fixture/model"})
	ctrl.SetToolApprovalMode(control.ToolApprovalDangerFullAccess)
	tab := &WorkspaceTab{ID: "tab", SessionID: op.SessionID, Ctrl: ctrl, model: "fixture/model", toolApprovalMode: control.ToolApprovalDangerFullAccess}
	a.tabs[tab.ID] = tab
	a.tabOrder = []string{tab.ID}
	settings := SessionDraftSettings{Model: "fixture/model", ToolApprovalMode: "read-only", CollaborationMode: "plan"}
	if err := a.prepareDraftRuntime(op, settings); err != nil {
		t.Fatal(err)
	}
	if ctrl.ToolApprovalMode() != normalizeToolApprovalMode(settings.ToolApprovalMode) || !ctrl.PlanMode() {
		t.Fatalf("actual runtime retained permission=%s plan=%v", ctrl.ToolApprovalMode(), ctrl.PlanMode())
	}
	if err := a.verifyDraftRuntime(tab.ID, settings); err != nil {
		t.Fatal(err)
	}
}
