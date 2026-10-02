package workspacestate

import "testing"

func TestPurgeRetainsOnlyTopicOwnershipInOperation(t *testing.T) {
	store, _ := seedArchivedProcessState(t)
	if err := store.EnsureSessionTopic(t.Context(), "victim", "original-topic", "Private title"); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginPurge(t.Context(), "victim", state.Generation); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvancePurge(t.Context(), "victim", "content_removed"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompletePurge(t.Context(), "victim"); err != nil {
		t.Fatal(err)
	}
	state, err = NewStore(store.Path()).Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	op := state.PendingOperations["purge-victim"]
	if op.WorkspaceID != GlobalWorkspaceID || op.Presentation == nil || op.Presentation.TopicID != "original-topic" || op.Presentation.Title != "" || op.Presentation.Pinned {
		t.Fatalf("invalid purge topic tombstone: %+v", op)
	}
	if _, ok := state.Presentation["victim"]; ok {
		t.Fatal("purge retained user presentation")
	}
	if len(state.Workspaces[GlobalWorkspaceID].SessionIDs) != 0 {
		t.Fatal("purge retained workspace membership")
	}
}
