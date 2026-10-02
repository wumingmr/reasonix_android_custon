package workspacestate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreationFollowsWorkspaceMergeBetweenReserveAndAttach(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "registry.json"))
	root := t.TempDir()
	if err := s.EnsureWorkspace(t.Context(), Workspace{ID: "old", Root: root}); err != nil {
		t.Fatal(err)
	}
	p := PendingCreate{OperationID: "create", WorkspaceID: "old", SessionID: "new-session"}
	if _, err := s.BeginCreateAtRoot(t.Context(), p, root); err != nil {
		t.Fatal(err)
	}
	// A historical writer had also registered Global for the same directory.
	if err := s.mutate(t.Context(), func(state *State) error {
		state.Workspaces[GlobalWorkspaceID] = Workspace{ID: GlobalWorkspaceID, Root: root}
		state.WorkspaceIDs = append(state.WorkspaceIDs, GlobalWorkspaceID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureWorkspaceResolved(t.Context(), Workspace{ID: GlobalWorkspaceID, Root: root}); err != nil {
		t.Fatal(err)
	}
	// Publication still carries the pre-merge ID. The registry must resolve it
	// inside its mutation, including when another process reopens the store.
	s = NewStore(s.Path())
	if err := s.AttachCreatedSessionAtRoot(t.Context(), p.OperationID, p.WorkspaceID, p.SessionID, root); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginCreateAtRoot(t.Context(), p, root); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachCreatedSessionAtRoot(t.Context(), p.OperationID, p.WorkspaceID, p.SessionID, root); err != nil {
		t.Fatal(err)
	}
	state, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertStrings(t, state.Workspaces[GlobalWorkspaceID].SessionIDs, []string{p.SessionID})
	if len(state.PendingCreates) != 0 || len(state.Workspaces) != 1 {
		t.Fatalf("duplicate state: %+v", state)
	}
}

func TestCreationTargetPreservesDirectoryAndLifecycleBoundaries(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "registry.json"))
	root := t.TempDir()
	if err := s.EnsureWorkspace(t.Context(), Workspace{ID: "project", Root: root}); err != nil {
		t.Fatal(err)
	}
	p := PendingCreate{OperationID: "create", WorkspaceID: "project", SessionID: "session"}
	if _, err := s.BeginCreateAtRoot(t.Context(), p, t.TempDir()); !errors.Is(err, ErrCreationWorkspaceChanged) {
		t.Fatalf("wrong directory: %v", err)
	}
	if _, err := s.BeginCreateAtRoot(t.Context(), p, filepath.Join(root, "missing")); !errors.Is(err, ErrCreationWorkspaceUnavailable) {
		t.Fatalf("missing directory: %v", err)
	}
	if _, err := s.BeginCreateAtRoot(t.Context(), p, root); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachCreatedSessionAtRoot(t.Context(), p.OperationID, p.WorkspaceID, p.SessionID, root); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveSession(t.Context(), p.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginCreateAtRoot(t.Context(), p, root); !errors.Is(err, ErrCreationSessionInactive) {
		t.Fatalf("archived reservation: %v", err)
	}
	if err := s.AttachCreatedSessionAtRoot(t.Context(), p.OperationID, p.WorkspaceID, p.SessionID, root); !errors.Is(err, ErrCreationSessionInactive) {
		t.Fatalf("archived publication: %v", err)
	}
	p.OperationID, p.SessionID = "fresh-click", "fresh-session"
	if _, err := s.BeginCreateAtRoot(t.Context(), p, root); err != nil {
		t.Fatal(err)
	}
	other := p
	other.OperationID = "competing-operation"
	if _, err := s.BeginCreateAtRoot(t.Context(), other, root); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("other operation: %v", err)
	}
	if err := s.mutate(t.Context(), func(state *State) error { delete(state.Workspaces, "project"); state.WorkspaceIDs = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginCreateAtRoot(t.Context(), p, root); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("removed registration: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("test removed physical directory", err)
	}
}
