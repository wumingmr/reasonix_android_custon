package workspacestate

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestPurgeCommandAdmissionDoesNotRelaxOtherCommands(t *testing.T) {
	s, expected := seedArchivedProcessState(t)
	if err := s.RenameWorkspace(t.Context(), GlobalWorkspaceID, "unrelated"); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginCommand(t.Context(), "command-restore", "restore", json.RawMessage(`{"action":"restore"}`), expected); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("restore gate relaxed: %v", err)
	}
	if err := s.BeginPurgeCommand(t.Context(), "command-wrong-action", "restore", json.RawMessage(`{"action":"restore"}`), expected); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("purge admitted restore: %v", err)
	}
	if err := s.BeginPurgeCommand(t.Context(), "command-future", "future", json.RawMessage(`{"action":"purge"}`), expected+100); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("future version admitted: %v", err)
	}
	if err := s.BeginPurgeCommand(t.Context(), "command-purge", "purge", json.RawMessage(`{"action":"purge"}`), expected); err != nil {
		t.Fatal(err)
	}
	state, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingOperations["command-purge"].ExpectedGeneration != expected || state.SessionStates["victim"].Lifecycle != Archived {
		t.Fatal("admission changed intent or lifecycle")
	}
}

func TestCommandChildRejectsInterveningLifecycle(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "registry.json"))
	ctx := t.Context()
	if err := s.EnsureWorkspace(ctx, Workspace{ID: "global", Root: t.TempDir(), Visible: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachSession(ctx, "", "global", "session", ""); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Load(ctx)
	if err := s.BeginCommand(ctx, "command-test", "fingerprint", json.RawMessage(`{}`), state.Generation); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveSession(ctx, "session"); err != nil {
		t.Fatal(err)
	}
	latest, _ := s.Load(ctx)
	// The RPC has a fresh snapshot, but still represents the old intent.
	err := s.BeginOperation(ctx, Operation{ID: "command-test-0", Kind: "restore", Lifecycle: Active, WorkspaceID: "global", SessionIDs: []string{"session"}, ExpectedGeneration: latest.Generation})
	if !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("stale command admitted: %v", err)
	}
	if err := s.BeginPurge(ctx, "session", state.Generation); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("stale purge admitted: %v", err)
	}
	if err := s.SaveCommandResult(ctx, "command-test", json.RawMessage(`{"generation":1}`), true); err != nil {
		t.Fatal(err)
	}
	latest, _ = s.Load(ctx)
	var result struct {
		Generation uint64 `json:"generation"`
	}
	if err := json.Unmarshal(latest.PendingOperations["command-test"].Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Generation != latest.Generation {
		t.Fatalf("receipt generation %d, commit %d", result.Generation, latest.Generation)
	}
}

func TestHistoricalArchiveCommitPreservesUnknownTime(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "registry.json"))
	ctx := t.Context()
	if err := s.EnsureWorkspace(ctx, Workspace{ID: "global", Root: t.TempDir(), Visible: true}); err != nil {
		t.Fatal(err)
	}
	op := Operation{ID: "legacy-trash", Kind: "archive-import", Lifecycle: Archived, WorkspaceID: "global", SessionIDs: []string{"old"}}
	if err := s.BeginOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	mapping := &SourceMapping{SourceKey: "old-source", Path: "/legacy/old.jsonl", Format: "legacy", Fingerprint: "proof", SessionID: "old"}
	if err := s.PrepareOperationContent(ctx, op.ID, op.SessionIDs, mapping, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitOperation(ctx, op.ID); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("batch guard lost: %v", err)
	}
	if err := s.CommitHistoricalArchive(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Load(ctx)
	if state.SessionStates["old"].Lifecycle != Archived || state.SessionStates["old"].ArchivedAt != 0 || state.PendingOperations[op.ID].Phase != "committed" {
		t.Fatalf("invalid historical commit: %+v", state)
	}
}
