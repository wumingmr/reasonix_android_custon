package workspacestate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadProjectionDoesNotCopyOrDiscardJournals(t *testing.T) {
	state := newState()
	state.Generation = 7
	state.Workspaces["w"] = Workspace{ID: "w", Title: "original", SessionIDs: []string{"s"}}
	state.SessionStates["s"] = SessionState{Lifecycle: "active"}
	state.PendingOperations["op"] = Operation{ID: "op", Lifecycle: "active", Kind: "import", Phase: "committed", Request: []byte(`{"large":"journal"}`)}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	projection, err := store.LoadProjection(t.Context())
	if err != nil || projection.Generation != 7 || len(projection.PendingOperations) != 0 {
		t.Fatalf("invalid display projection: %v", err)
	}
	projection.Workspaces["w"].SessionIDs[0] = "changed"
	full, err := store.Load(t.Context())
	if err != nil || full.Workspaces["w"].SessionIDs[0] != "s" || len(full.PendingOperations) != 1 {
		t.Fatalf("projection changed authoritative state: %v", err)
	}
}

func TestReadCacheObservesReplacementWithoutGenerationOrTimestampChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	body := []byte(`{"version":3,"generation":7,"workspaceIds":["global"],"workspaces":{"global":{"id":"global","title":"first","sessionIds":[]}},"sessionStates":{}}`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	if _, err := store.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte("first"), []byte("other"), 1)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stat.ModTime(), stat.ModTime()); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(t.Context())
	if err != nil || state.Workspaces["global"].Title != "other" {
		t.Fatalf("stale cache: %v", err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(t.Context()); err == nil {
		t.Fatal("cached state hid corruption")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	state, err = store.Load(t.Context())
	if err != nil || len(state.Workspaces) != 0 {
		t.Fatal("cached state survived deletion")
	}
}

func TestReadCacheIsolatesOperationAndUnknownFields(t *testing.T) {
	state := newState()
	state.PendingOperations["op"] = Operation{ID: "op", Request: []byte(`{"value":1}`), Result: []byte(`{"value":2}`),
		SessionIDs: []string{"s"}, Dependencies: []string{"dep"}, Mapping: &SourceMapping{RetainedArtifacts: []string{"artifact"}},
		Presentation: &Presentation{Title: "title"}}
	copy := cloneSnapshot(state)
	operation := copy.PendingOperations["op"]
	operation.Request[0], operation.Result[0] = '!', '!'
	operation.SessionIDs[0], operation.Dependencies[0] = "changed", "changed"
	operation.Mapping.RetainedArtifacts[0], operation.Presentation.Title = "changed", "changed"
	original := state.PendingOperations["op"]
	if original.Request[0] != '{' || original.Result[0] != '{' || original.SessionIDs[0] != "s" || original.Dependencies[0] != "dep" || original.Mapping.RetainedArtifacts[0] != "artifact" || original.Presentation.Title != "title" {
		t.Fatal("snapshot aliases cached operation storage")
	}
}
