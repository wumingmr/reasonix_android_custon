package workspacestate

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRecoveredSourceFencesCompetingOwnersAndGeneration(t *testing.T) {
	for _, retired := range []bool{false, true} {
		t.Run(fmt.Sprint(retired), func(t *testing.T) {
			ctx := t.Context()
			store := NewStore(filepath.Join(t.TempDir(), "state.json"))
			if err := store.EnsureWorkspace(ctx, Workspace{ID: GlobalWorkspaceID}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"old", "current"} {
				if err := store.AttachSession(ctx, "", GlobalWorkspaceID, id, ""); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(t.TempDir(), "source")
			mapping := SourceMapping{SourceKey: "old-key", Path: path, Format: "canonical", Fingerprint: "proof", SessionID: "old", WorkspaceID: GlobalWorkspaceID}
			if err := store.RecordSource(ctx, mapping, Presentation{}); err != nil {
				t.Fatal(err)
			}
			if retired {
				if err := store.ArchiveSession(ctx, "old"); err != nil {
					t.Fatal(err)
				}
				state, err := store.Load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.BeginPurge(ctx, "old", state.Generation); err != nil {
					t.Fatal(err)
				}
			}
			before, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			physical, err := sourcePathKey(path)
			if err != nil {
				t.Fatal(err)
			}
			mapping.SourceKey = fmt.Sprintf("%x", sha256.Sum256([]byte(physical+"\x00")))
			mapping.SessionID = "current"
			err = store.RecordRecoveredSource(ctx, mapping, before.Generation)
			if !retired {
				if !errors.Is(err, ErrMutationConflict) {
					t.Fatalf("stole live source: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			after, err := NewStore(store.Path()).Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.SessionStates, after.SessionStates) || !reflect.DeepEqual(before.Workspaces, after.Workspaces) || !reflect.DeepEqual(before.Presentation, after.Presentation) {
				t.Fatal("receipt repair changed target state")
			}
			if err := NewStore(store.Path()).ArchiveSession(ctx, "current"); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordRecoveredSource(ctx, mapping, after.Generation); !errors.Is(err, ErrMutationConflict) {
				t.Fatalf("stale lifecycle proof accepted: %v", err)
			}
		})
	}
}
