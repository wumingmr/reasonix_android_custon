package workspacestate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStorePersistsSessionOrderAndRecoverableArchive(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "workspace-state-v1.json"))
	ctx := t.Context()
	if err := store.EnsureWorkspace(ctx, Workspace{ID: "project-a", Root: t.TempDir(), Title: "A", Visible: true}); err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if err := store.BeginCreate(ctx, PendingCreate{OperationID: "op-1", WorkspaceID: "project-a", SessionID: "session-1"}); err != nil {
		t.Fatalf("BeginCreate: %v", err)
	}
	if err := store.AttachSession(ctx, "op-1", "project-a", "session-1", ""); err != nil {
		t.Fatalf("AttachSession(session-1): %v", err)
	}
	if err := store.BeginCreate(ctx, PendingCreate{OperationID: "op-2", WorkspaceID: "project-a", SessionID: "session-2"}); err != nil {
		t.Fatalf("BeginCreate: %v", err)
	}
	if err := store.AttachSession(ctx, "op-2", "project-a", "session-2", "session-1"); err != nil {
		t.Fatalf("AttachSession(session-2): %v", err)
	}

	if err := store.ArchiveSession(ctx, "session-2"); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load archived: %v", err)
	}
	assertStrings(t, state.Workspaces["project-a"].SessionIDs, []string{"session-2", "session-1"})
	assertStrings(t, state.ArchivedSessionIDs, []string{"session-2"})
	if len(state.PendingCreates) != 0 {
		t.Fatalf("pending creates = %#v", state.PendingCreates)
	}

	if err := store.RestoreSession(ctx, "session-2"); err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	reloaded, err := NewStore(store.Path()).Load(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	assertStrings(t, reloaded.Workspaces["project-a"].SessionIDs, []string{"session-2", "session-1"})
	if len(reloaded.ArchivedSessionIDs) != 0 {
		t.Fatalf("archived after restore = %#v", reloaded.ArchivedSessionIDs)
	}
}

func TestStoreCreateTransactionIsIdempotent(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "workspace-state-v1.json"))
	ctx := t.Context()
	if err := store.EnsureWorkspace(ctx, Workspace{ID: GlobalWorkspaceID, Title: "Global", Visible: true}); err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	pending := PendingCreate{OperationID: "op", WorkspaceID: GlobalWorkspaceID, SessionID: "session"}
	if err := store.BeginCreate(ctx, pending); err != nil {
		t.Fatalf("BeginCreate: %v", err)
	}
	if err := store.BeginCreate(ctx, pending); err != nil {
		t.Fatalf("repeat BeginCreate: %v", err)
	}
	if err := store.AttachSession(ctx, pending.OperationID, pending.WorkspaceID, pending.SessionID, ""); err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	if err := store.AttachSession(ctx, pending.OperationID, pending.WorkspaceID, pending.SessionID, ""); err != nil {
		t.Fatalf("repeat AttachSession: %v", err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	assertStrings(t, state.Workspaces[GlobalWorkspaceID].SessionIDs, []string{"session"})
}

func TestAbortCreateIfOperationCannotEraseNewerReservation(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "workspace-state-v1.json"))
	ctx := t.Context()
	if err := store.EnsureWorkspace(ctx, Workspace{ID: GlobalWorkspaceID, Title: "Global", Visible: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginCreate(ctx, PendingCreate{OperationID: "new", WorkspaceID: GlobalWorkspaceID, SessionID: "session"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AbortCreateIfOperation(ctx, "session", "old"); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending := state.PendingCreates["session"]; pending.OperationID != "new" {
		t.Fatalf("newer reservation was removed: %+v", pending)
	}
	if err := store.AbortCreateIfOperation(ctx, "session", "new"); err != nil {
		t.Fatal(err)
	}
	state, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.PendingCreates["session"]; ok {
		t.Fatal("matching reservation was not removed")
	}
}

func TestWorkspacePresentationCanBeRenamedHiddenAndReordered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace-state-v1.json")
	store := NewStore(path)
	ctx := t.Context()
	for _, workspace := range []Workspace{
		{ID: GlobalWorkspaceID, Title: "Global", Visible: true},
		{ID: "project-a", Title: "A", Visible: true},
		{ID: "project-b", Title: "B", Visible: true},
	} {
		if err := store.EnsureWorkspace(ctx, workspace); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RenameWorkspace(ctx, "project-b", "Renamed"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkspaceVisible(ctx, "project-a", false); err != nil {
		t.Fatal(err)
	}
	if err := store.MoveWorkspace(ctx, "project-b", GlobalWorkspaceID); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertStrings(t, state.WorkspaceIDs, []string{"project-b", GlobalWorkspaceID, "project-a"})
	if state.Workspaces["project-b"].Title != "Renamed" || state.Workspaces["project-a"].Visible {
		t.Fatalf("workspace presentation not persisted: %#v", state.Workspaces)
	}
	reopened, err := NewStore(path).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertStrings(t, reopened.WorkspaceIDs, []string{"project-b", GlobalWorkspaceID, "project-a"})
}

func TestCommitRotationAttachesReplacementAndArchivesSourceAtomically(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "workspace-state-v1.json"))
	ctx := t.Context()
	if err := store.EnsureWorkspace(ctx, Workspace{ID: "project-a", Visible: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSession(ctx, "", "project-a", "source", ""); err != nil {
		t.Fatal(err)
	}
	pending := PendingCreate{OperationID: "rotation", WorkspaceID: "project-a", SessionID: "replacement"}
	if err := store.BeginCreate(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRotation(ctx, pending.OperationID, pending.WorkspaceID, pending.SessionID, "", "source"); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertStrings(t, state.Workspaces["project-a"].SessionIDs, []string{"source", "replacement"})
	assertStrings(t, state.ArchivedSessionIDs, []string{"source"})
	if len(state.PendingCreates) != 0 {
		t.Fatalf("pending creates = %#v", state.PendingCreates)
	}
}

func TestAttachSessionFromSourceIfUnchangedRejectsArchivedSource(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "workspace-state-v1.json"))
	ctx := t.Context()
	if err := store.EnsureWorkspace(ctx, Workspace{ID: "project-a", Visible: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSession(ctx, "", "project-a", "source", ""); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	generation := state.SessionStates["source"].Generation
	if err := store.BeginCreate(ctx, PendingCreate{
		OperationID: "copy", WorkspaceID: "project-a", SessionID: "child",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveSession(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	err = store.AttachSessionFromSourceIfUnchanged(
		ctx, "copy", "project-a", "child", "", "source", generation,
	)
	if !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("guarded attach error = %v, want mutation conflict", err)
	}
	state, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if contains(state.Workspaces["project-a"].SessionIDs, "child") {
		t.Fatal("child attached after source lifecycle changed")
	}
	if _, pending := state.PendingCreates["child"]; !pending {
		t.Fatal("failed guarded attach discarded recovery journal")
	}
}

func TestStorePreservesUnknownTopLevelAndWorkspaceFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace-state-v1.json")
	original := `{"version":1,"generation":2,"initialized":true,"workspaceIds":["global"],"workspaces":{"global":{"id":"global","root":"","title":"Global","sessionIds":[],"visible":true,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","futureWorkspace":{"enabled":true}}},"archivedSessionIds":[],"pendingCreates":{},"futureTop":{"value":7}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store := NewStore(path)
	if err := store.RenameWorkspace(t.Context(), GlobalWorkspaceID, "Renamed"); err != nil {
		t.Fatalf("RenameWorkspace: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("Unmarshal top: %v", err)
	}
	if _, ok := decoded["futureTop"]; !ok {
		t.Fatal("futureTop was dropped")
	}
	var workspaces map[string]map[string]json.RawMessage
	if err := json.Unmarshal(decoded["workspaces"], &workspaces); err != nil {
		t.Fatalf("Unmarshal workspaces: %v", err)
	}
	if _, ok := workspaces[GlobalWorkspaceID]["futureWorkspace"]; !ok {
		t.Fatal("futureWorkspace was dropped")
	}
}

func TestStoreRefusesUnknownVersionWithoutOverwriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace-state-v1.json")
	original := []byte(`{"version":99,"future":true}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store := NewStore(path)
	if err := store.EnsureWorkspace(t.Context(), Workspace{ID: GlobalWorkspaceID}); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("EnsureWorkspace err = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(after) != string(original) {
		t.Fatalf("unknown version was overwritten: %s", after)
	}
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("strings = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("strings = %#v, want %#v", got, want)
		}
	}
}
