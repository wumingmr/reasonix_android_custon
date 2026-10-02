package workspacestate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	previous "reasonix/desktop/internal/workspacestate/testdata/v2previous"
)

func TestTopicAdoptionProjectionSurvivesPurgeAndReopen(t *testing.T) {
	state := newState()
	state.Workspaces["w"] = Workspace{ID: "w", SessionIDs: []string{"archived"}}
	state.Presentation["archived"] = Presentation{TopicID: "archived-topic"}
	state.SessionStates["archived"] = SessionState{Lifecycle: Archived}
	state.SessionStates["purged"] = SessionState{Lifecycle: Deleted}
	mapping := SourceMapping{SourceKey: "source", Path: "retained.jsonl", Format: "legacy", Fingerprint: "fingerprint", SessionID: "purged", WorkspaceID: "w"}
	state.SourceMappings[mapping.SourceKey] = mapping
	state.PendingOperations["import"] = Operation{ID: "import", Kind: "import", Lifecycle: Active, Phase: "committed", WorkspaceID: "w", Mapping: &mapping, Presentation: &Presentation{TopicID: "purged-topic"}}
	state.TopicRemovals["remove"] = TopicRemoval{ID: "remove", WorkspaceID: "w", TopicID: "removed-topic", Disposition: "archive_sessions", Phase: "committed"}
	state.TopicRemovals["pending"] = TopicRemoval{ID: "pending", WorkspaceID: "w", TopicID: "pending-topic", Disposition: "archive_sessions", Phase: "prepared"}
	state.TopicRemovals["placeholder"] = TopicRemoval{ID: "placeholder", WorkspaceID: "w", TopicID: "placeholder-topic", Disposition: "archive_placeholder", Phase: "restored"}
	state.PendingOperations["uncommitted"] = Operation{ID: "uncommitted", Kind: "import", Lifecycle: Active, Phase: "content_ready", WorkspaceID: "w", Mapping: &mapping, Presentation: &Presentation{TopicID: "not-adopted"}}
	// Historical registries can retain multiple source/head receipts for one
	// topic, old purge receipts with no presentation, and a failed new archive.
	// None of that should make the consumed topic into a fresh placeholder.
	head := mapping
	head.SourceKey, head.HeadID = "source-main", "main"
	state.SourceMappings[head.SourceKey] = head
	state.PendingOperations["import-main"] = Operation{ID: "import-main", Kind: "import", Lifecycle: Active, Phase: "committed", WorkspaceID: "w", Mapping: &head, Presentation: &Presentation{TopicID: "purged-topic"}}
	sibling := mapping
	sibling.SourceKey, sibling.SessionID = "source-sibling", "purged-sibling"
	state.SourceMappings[sibling.SourceKey] = sibling
	state.SessionStates[sibling.SessionID] = SessionState{Lifecycle: Deleted}
	state.PendingOperations["import-sibling"] = Operation{ID: "import-sibling", Kind: "import", Lifecycle: Archived, Phase: "committed", WorkspaceID: "w", Mapping: &sibling, Presentation: &Presentation{TopicID: "purged-topic"}}
	for _, id := range []string{"purged", "purged-sibling"} {
		state.PendingOperations["purge-"+id] = Operation{ID: "purge-" + id, Kind: "purge", Lifecycle: Deleted, Phase: "committed", SessionIDs: []string{id}}
	}
	state.TopicRemovals["retry-purged"] = TopicRemoval{ID: "retry-purged", WorkspaceID: "w", TopicID: "purged-topic", Disposition: "archive_sessions", Phase: "prepared"}
	want := map[string]bool{"archived-topic": true, "purged-topic": true, "removed-topic": true}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		store := NewStore(path)
		projection, err := store.LoadProjection(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := projection.AdoptedTopicIDs("w"); !reflect.DeepEqual(got, want) {
			t.Fatalf("adopted topics = %v; want %v", got, want)
		}
		if len(projection.AdoptedTopicIDs("other")) != 0 || len(projection.PendingOperations) != 0 || len(projection.TopicRemovals) != 0 {
			t.Fatal("projection leaked another workspace or operation payloads")
		}
		projection.AdoptedTopicIDs("w")["unrelated"] = true
		if projection.AdoptedTopicIDs("w")["unrelated"] {
			t.Fatal("caller mutated the cached adoption index")
		}
		full, err := store.Load(t.Context())
		if err != nil || !reflect.DeepEqual(full.AdoptedTopicIDs("w"), want) {
			t.Fatalf("full snapshot adoption differs: %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(body) {
			t.Fatal("projection rewrote registry bytes")
		}
	}
}

func TestPurgeRetainsTopicAdoptionWithoutImport(t *testing.T) {
	store, _ := seedArchivedProcessState(t)
	if err := store.EnsureSessionTopic(t.Context(), "victim", "legacy-topic", "Private title"); err != nil {
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
	// Resume completion with a new store to cover an interrupted purge.
	store = NewStore(store.Path())
	if err := store.CompletePurge(t.Context(), "victim"); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameWorkspace(t.Context(), GlobalWorkspaceID, "Unrelated edit"); err != nil {
		t.Fatal(err)
	}
	state, err = NewStore(store.Path()).Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !state.AdoptedTopicIDs(GlobalWorkspaceID)["legacy-topic"] || len(state.Workspaces[GlobalWorkspaceID].SessionIDs) != 0 || len(state.Presentation) != 0 {
		t.Fatal("purge lost topic adoption or retained live presentation")
	}
	receipt := state.PendingOperations["purge-victim"]
	if receipt.Presentation == nil || receipt.Presentation.Title != "" || receipt.Mapping != nil {
		t.Fatal("purge receipt retained more than topic identity")
	}
	// These are existing operation fields, including for older codecs. The
	// registry schema fence remains unchanged; this checks the receipt shape.
	body, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var old previous.Operation
	if err := json.Unmarshal(body, &old); err != nil {
		t.Fatal(err)
	}
	roundtrip, err := json.Marshal(old)
	if err != nil || string(roundtrip) != string(body) {
		t.Fatal("previous operation codec changed the purge receipt")
	}
}
