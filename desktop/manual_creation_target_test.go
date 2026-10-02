package main

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
)

// The registry has already merged an old workspace ID into its canonical
// owner, but the independent creation journal still contains that old ID.
func seedAbsorbedManualCreation(t *testing.T, a *App, id string) ManualSessionCreationView {
	t.Helper()
	v, r := seedManualCreation(t, a, id, "reserved")
	if _, err := ensureGlobalWorkspaceRoot(); err != nil {
		t.Fatal(err)
	}
	v.WorkspaceID = "absorbed-workspace"
	v.Scope, v.WorkspaceRoot = "project", globalWorkspaceRoot()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.sessionUIStore().Save(t.Context(), "creation", id, r.Revision, body); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestManualCreationAutomaticallyRetriesUnavailableDirectory(t *testing.T) {
	a := newManualSessionTestApp(t)
	v, _ := seedManualCreation(t, a, "directory-reappears", "reserved")
	m := a.creationManager()
	var attempts atomic.Int32
	m.execute = func(_ context.Context, got ManualSessionCreationView, _ func(string)) error {
		if got.Ref != v.Ref || got.OperationID != v.OperationID {
			return errManualCreationIdentity
		}
		if attempts.Add(1) == 1 {
			return manualCreationTargetError(workspacestate.ErrCreationWorkspaceUnavailable)
		}
		return nil
	}
	m.Ensure(v.OperationID, "begin", "")
	a.manualCreationTasks.Wait()
	got, err := a.GetManualSessionCreation(v.OperationID)
	if err != nil || got.Phase != "ready" || attempts.Load() != 2 {
		t.Fatalf("automatic retry = %+v attempts=%d %v", got, attempts.Load(), err)
	}
	r, err := a.sessionUIStore().Get(t.Context(), "creation", v.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(r.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["future"]) != `{"preserve":true}` || fields["surfaceReady"] != nil || fields["progress"] != nil {
		t.Fatalf("persisted contract changed: %s", r.Payload)
	}
}

func TestManualCreationRecoversPreviouslyFailedTargetChange(t *testing.T) {
	a := newManualSessionTestApp(t)
	v := seedAbsorbedManualCreation(t, a, "old-target-changed-failure")
	r, err := a.sessionUIStore().Get(t.Context(), "creation", v.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	m := a.creationManager()
	if _, err := m.savePhase(r, "failed", "session_operation:target_changed:old failure"); err != nil {
		t.Fatal(err)
	}
	m.scan()
	a.manualCreationTasks.Wait()
	got, err := a.GetManualSessionCreation(v.OperationID)
	if err != nil || got.Phase != "ready" || got.Ref != v.Ref {
		t.Fatalf("old failed request = %+v %v", got, err)
	}
}

func TestManualCreationOpensInputBeforeRuntimeReady(t *testing.T) {
	a := newManualSessionTestApp(t)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	a.tabBuildStartHook = func(string) { close(started); <-release }
	v, err := a.BeginManualSessionCreation(ManualSessionCreationRequest{OperationID: "input-before-runtime", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	v, err = a.GetManualSessionCreation(v.OperationID)
	if err != nil || !v.SurfaceReady || v.Phase == "ready" {
		t.Fatalf("early surface = %+v %v", v, err)
	}
	opened := make(chan error, 1)
	go func() { _, err := a.OpenSession(v.Ref); opened <- err }()
	select {
	case err := <-opened:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		unblock()
		<-opened
		t.Fatal("opening the input waited for runtime initialization")
	}
	a.mu.RLock()
	early := a.activeTabLocked()
	preservedBuild := early != nil && early.SessionID == v.Ref.SessionID && early.Ctrl == nil && early.buildExecution != nil && len(a.tabs) == 1
	a.mu.RUnlock()
	if !preservedBuild {
		t.Fatal("opening input built a competing runtime")
	}
	input, err := a.GetSessionComposerState(v.Ref)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.SaveSessionComposerState(SessionComposerSaveRequest{Ref: v.Ref, ExpectedRevision: input.Revision, ContentVersion: 1, ContentJSON: `{"text":"typed while starting"}`})
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	a.manualCreationTasks.Wait()
	input, err = a.GetSessionComposerState(v.Ref)
	if err != nil || input.ContentJSON != `{"text":"typed while starting"}` {
		t.Fatalf("input lost: %+v %v", input, err)
	}
	a.mu.RLock()
	active := a.activeTabLocked()
	correct := active != nil && active.SessionID == v.Ref.SessionID && active.Ctrl != nil
	a.mu.RUnlock()
	if !correct {
		t.Fatal("initialization did not preserve the selected session")
	}
}

func TestManualCreationResolvesAbsorbedWorkspaceOnRecovery(t *testing.T) {
	a := newManualSessionTestApp(t)
	v := seedAbsorbedManualCreation(t, a, "absorbed-create-recovery")
	a.creationManager().Ensure(v.OperationID, "recovery", "")
	a.manualCreationTasks.Wait()
	got, err := a.GetManualSessionCreation(v.OperationID)
	if err != nil || got.Phase != "ready" || got.Ref != v.Ref {
		t.Fatalf("recovery changed identity or failed: %+v %v", got, err)
	}
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if ids := state.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs; len(ids) != 1 || ids[0] != v.Ref.SessionID {
		t.Fatalf("canonical membership = %v", ids)
	}
}

func TestManualCreationReplayResolvesAbsorbedWorkspace(t *testing.T) {
	a := newManualSessionTestApp(t)
	v := seedAbsorbedManualCreation(t, a, "absorbed-create-replay")
	for _, id := range []string{workspacestate.GlobalWorkspaceID, v.WorkspaceID} {
		got, err := a.BeginManualSessionCreation(ManualSessionCreationRequest{
			OperationID: v.OperationID, WorkspaceID: id, Scope: "project", WorkspaceRoot: v.WorkspaceRoot,
		})
		if err != nil || got.Ref != v.Ref {
			t.Fatalf("replay %s = %+v %v", id, got, err)
		}
	}
	a.manualCreationTasks.Wait()
}

func TestManualCreationReplayDoesNotRegisterDifferentWorkspace(t *testing.T) {
	a := newManualSessionTestApp(t)
	v, _ := seedManualCreation(t, a, "replay-different-directory", "ready")
	before, err := a.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.BeginManualSessionCreation(ManualSessionCreationRequest{
		OperationID: v.OperationID, Scope: "project", WorkspaceRoot: t.TempDir(),
	})
	if err == nil {
		t.Fatal("same operation accepted a different directory")
	}
	after, err := a.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Workspaces) != len(before.Workspaces) {
		t.Fatal("replay registered an unrelated workspace")
	}
}
