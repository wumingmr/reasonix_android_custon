package main

import (
	"testing"

	"reasonix/desktop/internal/workspacestate"
)

// Exercise the reported ordering on disposable state, including a retired
// draft database that must not participate in either creation or reopening.
func TestManualCreationAfterArchive(t *testing.T) {
	for _, scope := range []string{"global", "project"} {
		t.Run(scope, func(t *testing.T) {
			a := newManualSessionTestApp(t)
			corruptRetiredDraftDatabase(t)
			root := ""
			if scope == "project" {
				root = t.TempDir()
			}
			create := func(id string) ManualSessionCreationView {
				t.Helper()
				if _, err := a.BeginManualSessionCreation(ManualSessionCreationRequest{OperationID: id, Scope: scope, WorkspaceRoot: root}); err != nil {
					t.Fatal(err)
				}
				a.manualCreationTasks.Wait()
				view, err := a.GetManualSessionCreation(id)
				if err != nil || view.Phase != "ready" {
					t.Fatalf("creation: %+v %v", view, err)
				}
				if _, err := a.OpenSession(view.Ref); err != nil {
					t.Fatal(err)
				}
				return view
			}
			first := create("manual-before-archive")
			receipt, err := a.ArchiveSessionTarget(SessionSelector{Ref: &first.Ref})
			if err != nil || !receipt.Committed {
				t.Fatalf("archive: %+v %v", receipt, err)
			}
			assertNoVisibleRuntime(t, a)
			second := create("manual-after-archive")
			if second.Ref == first.Ref {
				t.Fatal("new creation reused the archived identity")
			}
			state, err := a.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if state.SessionStates[first.Ref.SessionID].Lifecycle != workspacestate.Archived || state.SessionStates[second.Ref.SessionID].Lifecycle != workspacestate.Active {
				t.Fatal("archive/new lifecycle did not remain independent")
			}
			pending, err := a.ListManualSessionCreations()
			if err != nil || len(pending) != 0 {
				t.Fatalf("creation recovery remained: %+v %v", pending, err)
			}
		})
	}
}
