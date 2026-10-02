package workspacestate

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadSnapshotsAreIndependentAndObserveOtherWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	original := []byte(`{"version":3,"generation":7,"workspaceIds":["global"],"workspaces":{"global":{"id":"global","title":"Global","sessionIds":["s"],"organization":{"revision":3,"order":["session:s"],"groups":[],"imported":{}},"futureWorkspace":{"keep":true}}},"sessionStates":{"s":{"lifecycle":"active"}},"futureRoot":{"keep":true}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	first, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Exercise maps, slices, nested pointers and retained unknown JSON bytes.
	first.WorkspaceIDs[0] = "changed"
	workspace := first.Workspaces[GlobalWorkspaceID]
	workspace.SessionIDs[0] = "changed"
	workspace.Organization.Order[0] = "changed"
	workspace.extra["futureWorkspace"][0] = '!'
	first.extra["futureRoot"][0] = '!'
	delete(first.SessionStates, "s")
	third, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, third) {
		t.Fatal("mutating one snapshot changed another read")
	}
	body, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(body, original) {
		t.Fatalf("read-only loads changed the source: %v", err)
	}
	// A separate Store represents another process writing the same registry.
	if err := NewStore(path).RenameWorkspace(t.Context(), GlobalWorkspaceID, "Updated"); err != nil {
		t.Fatal(err)
	}
	latest, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if latest.Generation != second.Generation+1 || latest.Workspaces[GlobalWorkspaceID].Title != "Updated" {
		t.Fatal("existing reader did not observe the external write")
	}
	if second.Workspaces[GlobalWorkspaceID].Title != "Global" {
		t.Fatal("external write changed a previously returned snapshot")
	}
	if !bytes.Equal(latest.extra["futureRoot"], second.extra["futureRoot"]) ||
		!bytes.Equal(latest.Workspaces[GlobalWorkspaceID].extra["futureWorkspace"], second.Workspaces[GlobalWorkspaceID].extra["futureWorkspace"]) {
		t.Fatal("external mutation lost unknown fields")
	}
}
