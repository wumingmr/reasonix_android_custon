package workspacestate

import (
	"errors"
	"path/filepath"
	"testing"

	"reasonix/internal/filelock"
)

func TestMetadataCommitHoldsLifecycleFileLockAndRejectsArchiveRestore(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.EnsureWorkspace(t.Context(), Workspace{ID: "global"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSession(t.Context(), "", "global", "target", ""); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	generation := state.SessionStates["target"].Generation
	if err := store.WithSessionUnchanged(t.Context(), "target", "global", generation, func() error {
		release, err := filelock.TryAcquire(store.Path() + ".lock")
		if err == nil {
			release()
			t.Fatal("metadata commit did not hold the cross-process lifecycle lock")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	other := NewStore(store.Path())
	if err := other.ArchiveSession(t.Context(), "target"); err != nil {
		t.Fatal(err)
	}
	commit := func() error { t.Fatal("stale metadata callback was invoked"); return nil }
	if err := store.WithSessionUnchanged(t.Context(), "target", "global", generation, commit); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("archive: %v", err)
	}
	if err := other.RestoreSession(t.Context(), "target"); err != nil {
		t.Fatal(err)
	}
	if err := store.WithSessionUnchanged(t.Context(), "target", "global", generation, commit); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("archive/restore ABA: %v", err)
	}
}
