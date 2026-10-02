package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

func TestEmptyTrashDoesNotResurrectManualTopics(t *testing.T) {
	for _, scope := range []string{"global", "project"} {
		t.Run(scope, func(t *testing.T) { checkEmptyTrashDoesNotResurrectManualTopics(t, scope) })
	}
}

func TestPurgeSharedTopicKeepsSurvivingSession(t *testing.T) {
	a := newSavedTabReconcileTestApp(t)
	t.Cleanup(a.closeSessionServices)
	root := t.TempDir()
	topic, err := a.CreateTopic("project", root, "Shared history")
	if err != nil {
		t.Fatal(err)
	}
	var refs []session.SessionRef
	for _, id := range []string{"purge-shared-old", "purge-shared-live"} {
		ref, _ := createLegacyCleanupSession(t, a, root, id, true)
		if err := a.workspaceRegistry().EnsureSessionTopic(t.Context(), id, topic.ID, "Shared history"); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	for index, ref := range refs {
		if err := a.ArchiveCanonicalSession(ref); err != nil {
			t.Fatal(err)
		}
		if err := a.PurgeCanonicalSession(ref); err != nil {
			t.Fatal(err)
		}
		page, err := a.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			if len(page.Items) != 1 || page.Items[0].Session == nil || *page.Items[0].Session != refs[1] {
				t.Fatalf("shared live history hidden: %+v", page)
			}
		} else if len(page.Items) != 0 {
			t.Fatalf("last purged session left a placeholder: %+v", page)
		}
	}
	if _, err := a.ActivateTopic("project", root, topic.ID, ""); err == nil {
		t.Fatal("purged non-manual topic revived")
	}
}

func TestPurgedTopicEvidencePreservesSurvivingOwners(t *testing.T) {
	state := workspacestate.State{
		SessionStates:     map[string]workspacestate.SessionState{},
		Presentation:      map[string]workspacestate.Presentation{},
		PendingOperations: map[string]workspacestate.Operation{},
		PendingCreates:    map[string]workspacestate.PendingCreate{},
	}
	// An older import operation can retain exact topic ownership even if its
	// previous purge writer omitted that evidence from the purge operation.
	state.SessionStates["old-import"] = workspacestate.SessionState{Lifecycle: workspacestate.Deleted}
	state.PendingOperations["import"] = workspacestate.Operation{Kind: "import", Phase: "committed", SessionIDs: []string{"old-import"}, Presentation: &workspacestate.Presentation{TopicID: "imported-topic"}}
	if !purgedCanonicalTopicIDs(state)["imported-topic"] {
		t.Fatal("lost previous import tombstone evidence")
	}
	state.PendingCreates["new"] = workspacestate.PendingCreate{Presentation: &workspacestate.Presentation{TopicID: "imported-topic"}}
	if purgedCanonicalTopicIDs(state)["imported-topic"] {
		t.Fatal("pending owner was hidden")
	}
	delete(state.PendingCreates, "new")
	state.SessionStates["restored"] = workspacestate.SessionState{Lifecycle: workspacestate.Active}
	state.Presentation["restored"] = workspacestate.Presentation{TopicID: "imported-topic"}
	if purgedCanonicalTopicIDs(state)["imported-topic"] {
		t.Fatal("active owner was hidden")
	}
}

func checkEmptyTrashDoesNotResurrectManualTopics(t *testing.T, scope string) {
	a := newManualSessionTestApp(t)
	root := ""
	if scope == "project" {
		root = t.TempDir()
	}
	var refs []session.SessionRef
	var topics []string
	for i := range 4 {
		view, err := a.BeginManualSessionCreation(ManualSessionCreationRequest{OperationID: fmt.Sprintf("purge-sidebar-%d", i), Scope: scope, WorkspaceRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, view.Ref)
		topics = append(topics, view.TopicID)
	}
	a.manualCreationTasks.Wait()
	for _, ref := range refs {
		if err := a.ArchiveCanonicalSession(ref); err != nil {
			t.Fatal(err)
		}
	}
	assertEmpty := func(app *App, stage string) {
		t.Helper()
		page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: scope, WorkspaceRoot: root, Limit: 50})
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("%s sidebar resurrected topics: %+v %v", stage, page.Items, err)
		}
	}
	assertEmpty(a, "archived")
	trash, err := a.ListTrashEntries("", "", 50)
	if err != nil || len(trash.Items) != 4 {
		t.Fatalf("trash: %+v %v", trash, err)
	}
	request := SessionLifecycleRequest{OperationID: "empty-sidebar-trash", Action: "purge", ExpectedGeneration: trash.Generation}
	for _, row := range trash.Items {
		request.Targets = append(request.Targets, SessionLifecycleTarget{Ref: row.Ref, WorkspaceID: row.WorkspaceID})
	}
	result, err := a.ApplySessionLifecycle(request)
	if err != nil || !result.Committed {
		t.Fatalf("empty trash: %+v %v", result, err)
	}
	assertEmpty(a, "purged")
	for range 2 {
		for _, topic := range topics {
			if _, err := a.ActivateTopic(scope, root, topic, ""); err == nil {
				t.Fatal("stale deleted topic click created a replacement session")
			}
		}
	}
	assertEmpty(a, "after stale clicks")
	waitForInitialCatalogReconcile(t, a)
	t.Cleanup(func() { a.stopSessionCatalog(time.Second) })
	assertEmpty(a, "catalog ready")
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.SessionStates) != 4 {
		t.Fatalf("stale click added sessions: %+v", state.SessionStates)
	}
	for _, ref := range refs {
		op := state.PendingOperations["purge-"+ref.SessionID]
		if op.Presentation == nil || op.Presentation.TopicID == "" {
			t.Fatal("purge lost topic ownership")
		}
		// Simulate a previous writer that erased topic ownership on purge.
		op.Presentation, op.WorkspaceID = nil, ""
		state.PendingOperations[op.ID] = op
	}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.workspaceRegistry().Path(), body, 0600); err != nil {
		t.Fatal(err)
	}
	reopened := NewApp()
	reopened.ctx = t.Context()
	reopened.desktopSessions.root = a.desktopSessions.root
	installNoopRuntimeEvents(reopened)
	t.Cleanup(func() {
		reopened.closeSessionServices()
		_ = reopened.sessionUI.Close()
		_ = reopened.desktopDrafts.Close()
	})
	assertEmpty(reopened, "restart with previous-writer tombstones")
	if _, err := reopened.ActivateTopic(scope, root, topics[0], ""); err == nil {
		t.Fatal("previous-writer stale topic revived after restart")
	}
	placeholder, err := reopened.CreateTopic(scope, root, "Untouched empty topic")
	if err != nil {
		t.Fatal(err)
	}
	page, err := reopened.ListProjectTopics(ProjectTopicPageRequest{Scope: scope, WorkspaceRoot: root, Limit: 50})
	if err != nil || len(page.Items) != 1 || page.Items[0].TopicID != placeholder.ID {
		t.Fatalf("unrelated placeholder lost: %+v %v", page, err)
	}
}
