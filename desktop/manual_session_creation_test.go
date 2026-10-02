package main

import (
	"encoding/json"
	"testing"

	"reasonix/desktop/internal/sessionui"
	"reasonix/internal/session"
)

func newManualSessionTestApp(t *testing.T) *App {
	t.Helper()
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.ctx = t.Context()
	installNoopRuntimeEvents(a)
	t.Cleanup(func() {
		if err := a.stopManualCreations(); err != nil {
			t.Fatal(err)
		}
		for _, tab := range a.tabs {
			if tab.Ctrl != nil {
				tab.Ctrl.Close()
				if closed, ok := tab.Ctrl.(interface{ Closed() <-chan struct{} }); ok {
					<-closed.Closed()
				}
			}
		}
		a.closeSessionServices()
		_ = a.sessionUI.Close()
		_ = a.desktopDrafts.Close()
	})
	return a
}

func TestManualCreationUniqueIdempotentAndVisibleBeforeFirstSend(t *testing.T) {
	a := newManualSessionTestApp(t)
	for _, id := range []string{"manual-test-one", "manual-test-two", "manual-test-three"} {
		first, err := a.BeginManualSessionCreation(ManualSessionCreationRequest{OperationID: id, Scope: "global"})
		if err != nil {
			t.Fatal(err)
		}
		retry, err := a.BeginManualSessionCreation(ManualSessionCreationRequest{OperationID: id, Scope: "global"})
		if err != nil || retry.Ref != first.Ref {
			t.Fatalf("replayed identity=%+v %v", retry, err)
		}
	}
	a.manualCreationTasks.Wait()
	for _, id := range []string{"manual-test-one", "manual-test-two", "manual-test-three"} {
		view, err := a.GetManualSessionCreation(id)
		if err != nil || view.Phase != "ready" {
			t.Fatalf("runtime did not start: %+v %v", view, err)
		}
	}
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Workspaces["global"].SessionIDs) != 3 {
		t.Fatalf("session ids=%v", state.Workspaces["global"].SessionIDs)
	}
	for _, id := range state.Workspaces["global"].SessionIDs {
		snapshot, err := a.desktopSessionService("").Query().Snapshot(t.Context(), session.SessionRef{HostID: "local", SessionID: id})
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range snapshot.Projection.Messages {
			if message.Role != "system" {
				t.Fatal("creation submitted a non-system message")
			}
		}
	}
	if drafts, err := a.ListSessionDraftSummaries(); err != nil || len(drafts) != 0 {
		t.Fatalf("new path created drafts: %+v %v", drafts, err)
	}
}

func TestComposerRestartConflictAndUnknownSubmission(t *testing.T) {
	a := newManualSessionTestApp(t)
	op, err := a.BeginManualSessionCreation(ManualSessionCreationRequest{OperationID: "composer-test-create", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	a.manualCreationTasks.Wait()
	input, err := a.GetSessionComposerState(op.Ref)
	if err != nil {
		t.Fatal(err)
	}
	req := SessionComposerSaveRequest{Ref: op.Ref, ExpectedRevision: input.Revision, ContentVersion: 1, ContentJSON: `{"text":"keep me","attachments":[],"future":{"keep":true}}`}
	saved, err := a.SaveSessionComposerState(req)
	if err != nil || saved.Conflict {
		t.Fatalf("save=%+v %v", saved, err)
	}
	conflict, err := a.SaveSessionComposerState(req)
	if err != nil || !conflict.Conflict {
		t.Fatalf("CAS=%+v %v", conflict, err)
	}
	path := a.sessionUI.Path()
	_ = a.sessionUI.Close()
	a.sessionUI = sessionui.New(path)
	restored, err := a.GetSessionComposerState(op.Ref)
	if err != nil || restored.ContentJSON != req.ContentJSON {
		t.Fatalf("restored=%+v %v", restored, err)
	}
	pending, err := a.BeginSessionComposerSubmission(op.Ref, saved.Revision, "composer-pending", `{"input":"hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := a.GetSessionComposerState(op.Ref)
	if err != nil || unknown.SubmissionPhase != "unknown" || unknown.ContentJSON != req.ContentJSON {
		t.Fatalf("unknown=%+v %v", unknown, err)
	}
	req.ExpectedRevision = pending.Revision
	if _, err := a.SaveSessionComposerState(req); err == nil {
		t.Fatal("pending input was overwritten")
	}
	accepted, err := a.CompleteSessionComposerSubmission(op.Ref, "composer-pending", "accepted")
	if err != nil || accepted.ContentJSON != "{}" {
		t.Fatalf("accepted=%+v %v", accepted, err)
	}
	if !json.Valid([]byte(accepted.ContentJSON)) {
		t.Fatal("invalid saved content")
	}
}

func TestPreviousDraftAPIWillNotCreateNewRecords(t *testing.T) {
	a := newManualSessionTestApp(t)
	if _, err := a.OpenSessionDraftForTarget("global", ""); err == nil {
		t.Fatal("legacy API created a draft")
	}
	rows, err := a.ListSessionDraftSummaries()
	if err != nil || len(rows) != 0 {
		t.Fatalf("legacy rows=%+v %v", rows, err)
	}
}

func TestManualCreationCancelledBuildRetainsIdentityForRetry(t *testing.T) {
	a := newManualSessionTestApp(t)
	started, release := make(chan struct{}), make(chan struct{})
	a.tabBuildStartHook = func(string) { close(started); <-release }
	operation, err := a.BeginManualSessionCreation(ManualSessionCreationRequest{OperationID: "cancelled-build-operation", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	a.shuttingDown.Store(true)
	a.cancelAllTabBuilds()
	close(release)
	a.manualCreationTasks.Wait()
	failed, err := a.GetManualSessionCreation(operation.OperationID)
	if err != nil || failed.Phase != "starting" || failed.Ref != operation.Ref {
		t.Fatalf("cancelled creation=%+v %v", failed, err)
	}
	a.shuttingDown.Store(false)
	a.tabBuildStartHook = nil
	if _, err := a.RetryManualSessionCreation(operation.OperationID); err != nil {
		t.Fatal(err)
	}
	a.manualCreationTasks.Wait()
	retried, err := a.GetManualSessionCreation(operation.OperationID)
	if err != nil || retried.Phase != "ready" || retried.Ref != operation.Ref {
		t.Fatalf("retry=%+v %v", retried, err)
	}
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil || len(state.Workspaces["global"].SessionIDs) != 1 {
		t.Fatalf("retry created replacement: %+v %v", state, err)
	}
}
