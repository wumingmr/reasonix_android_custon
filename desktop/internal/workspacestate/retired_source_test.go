package workspacestate

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRetiredSourceReceiptPreservesLifecycleAndPresentation(t *testing.T) {
	for _, lifecycle := range []string{Archived, Deleted} {
		t.Run(lifecycle, func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), "state.json"))
			ctx := t.Context()
			if err := store.EnsureWorkspace(ctx, Workspace{ID: GlobalWorkspaceID, Visible: true}); err != nil {
				t.Fatal(err)
			}
			if err := store.AttachSession(ctx, "", GlobalWorkspaceID, "original", ""); err != nil {
				t.Fatal(err)
			}
			if err := store.ArchiveSession(ctx, "original"); err != nil {
				t.Fatal(err)
			}
			before, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if lifecycle == Deleted {
				if err := store.BeginPurge(ctx, "original", before.Generation); err != nil {
					t.Fatal(err)
				}
				before, err = store.Load(ctx)
				if err != nil {
					t.Fatal(err)
				}
			}
			mapping := SourceMapping{SourceKey: "canonical-copy", Path: "/source", Format: "canonical", Fingerprint: "actual-file-fingerprint", SessionID: "original", WorkspaceID: GlobalWorkspaceID}
			if err := store.RecordRetiredSource(ctx, mapping, before.Generation); err != nil {
				t.Fatal(err)
			}
			after, err := NewStore(store.Path()).Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.SessionStates, after.SessionStates) || !reflect.DeepEqual(before.Workspaces, after.Workspaces) ||
				!reflect.DeepEqual(before.Presentation, after.Presentation) || !reflect.DeepEqual(before.PendingOperations, after.PendingOperations) {
				t.Fatal("receipt repair mutated lifecycle, membership, presentation or journal")
			}
			if err := store.RecordRetiredSource(ctx, mapping, before.Generation); !errors.Is(err, ErrMutationConflict) {
				t.Fatalf("stale proof accepted: %v", err)
			}
			if err := store.RecordRetiredSource(ctx, mapping, after.Generation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestArchiveTransferRejectsChangedReservation(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	ctx := t.Context()
	if err := store.EnsureWorkspace(ctx, Workspace{ID: GlobalWorkspaceID}); err != nil {
		t.Fatal(err)
	}
	op := Operation{ID: "import-source", Kind: "import", Lifecycle: Active, WorkspaceID: GlobalWorkspaceID, SessionIDs: []string{"copy"},
		Mapping: &SourceMapping{SourceKey: "source", Fingerprint: "fingerprint", SessionID: "copy", WorkspaceID: GlobalWorkspaceID}}
	if err := store.BeginOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observed := state.PendingOperations[op.ID]
	if err := store.StageHistoricalArchive(ctx, observed, state.Generation); err != nil {
		t.Fatal(err)
	}
	if err := store.StageHistoricalArchive(ctx, observed, state.Generation); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("stale transfer: %v", err)
	}
	state, err = store.Load(ctx)
	if err != nil || state.PendingOperations[op.ID].Kind != "archive-import" || len(state.SessionStates) != 0 {
		t.Fatalf("transfer published membership: %v", err)
	}
	if err := store.CommitOperation(ctx, op.ID); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("orphan child committed: %v", err)
	}
}

func TestArchiveReservationFencesOtherImportKinds(t *testing.T) {
	for _, kind := range []string{"import", "archive-import", "restore"} {
		t.Run(kind, func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), "state.json"))
			ctx := t.Context()
			if err := store.EnsureWorkspace(ctx, Workspace{ID: GlobalWorkspaceID}); err != nil {
				t.Fatal(err)
			}
			reserved := Operation{ID: "archive-child", Kind: "archive-import", Lifecycle: Archived,
				WorkspaceID: GlobalWorkspaceID, SessionIDs: []string{"reserved"},
				Mapping: &SourceMapping{SourceKey: "source", Fingerprint: "fingerprint", SessionID: "reserved", WorkspaceID: GlobalWorkspaceID}}
			if err := store.BeginOperation(ctx, reserved); err != nil {
				t.Fatal(err)
			}
			other := reserved
			other.ID, other.Kind = "competing-operation", kind
			if err := store.BeginOperation(ctx, other); !errors.Is(err, ErrMutationConflict) {
				t.Fatalf("%s stole archive reservation: %v", kind, err)
			}
		})
	}
}
