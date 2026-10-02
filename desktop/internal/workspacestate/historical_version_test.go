package workspacestate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func conflictingHistoricalVersionFixture(t *testing.T) (*Store, Operation) {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), "workspace-state-v1.json"))
	if err := store.EnsureWorkspace(t.Context(), Workspace{ID: "global", Root: t.TempDir(), Visible: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSession(t.Context(), "", "global", "original", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSource(t.Context(), SourceMapping{SourceKey: "source", Path: "/old/source", Format: "canonical", Fingerprint: "old", SessionID: "original", WorkspaceID: "global"}, Presentation{}); err != nil {
		t.Fatal(err)
	}
	op := Operation{ID: "import-source-new", Kind: "import", Lifecycle: Active, WorkspaceID: "global", SessionIDs: []string{"reserved"}}
	if err := store.BeginOperation(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	mapping := &SourceMapping{SourceKey: "source", Path: "/old/source", Format: "canonical", Fingerprint: "new", SessionID: "reserved", WorkspaceID: "global", extra: map[string]json.RawMessage{"futureMapping": json.RawMessage(`{"keep":true}`)}}
	if err := store.PrepareOperationContent(t.Context(), op.ID, op.SessionIDs, mapping, &Presentation{Title: "Branch", extra: map[string]json.RawMessage{"futureTitle": json.RawMessage(`42`)}}); err != nil {
		t.Fatal(err)
	}
	if err := store.mutate(t.Context(), func(s *State) error {
		v := s.PendingOperations[op.ID]
		v.extra = map[string]json.RawMessage{"futureOperation": json.RawMessage(`"keep"`)}
		s.PendingOperations[op.ID] = v
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return store, state.PendingOperations[op.ID]
}

func TestCommitHistoricalVersionPreservesOriginalAndBacksUp(t *testing.T) {
	store, op := conflictingHistoricalVersionFixture(t)
	before, _ := os.ReadFile(store.Path())
	state, _ := store.Load(t.Context())
	original := state.SourceMappings["source"]
	if err := store.CommitHistoricalVersion(t.Context(), op, "source:review:new"); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(store.Path()).Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, reopened.SourceMappings["source"]) || reopened.PendingOperations[op.ID].Phase != "committed" || reopened.SourceMappings["source:review:new"].SessionID != "reserved" {
		t.Fatal("version commit changed original adoption or lost the branch")
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(store.Path()), "historical-version-backups", "*.json"))
	if len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}
	saved, _ := os.ReadFile(backups[0])
	if !bytes.Equal(saved, before) {
		t.Fatal("backup changed original bytes")
	}
	after, _ := os.ReadFile(store.Path())
	for _, key := range []string{"futureMapping", "futureOperation", "futureTitle"} {
		if !bytes.Contains(after, []byte(key)) {
			t.Fatalf("lost unknown metadata %q", key)
		}
	}
	// Ordinary commit replay remains idempotent for older readers/writers.
	if err := NewStore(store.Path()).CommitOperation(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(store.Path())
	if !bytes.Equal(after, again) {
		t.Fatal("committed replay rewrote state")
	}
}

func TestCommitHistoricalVersionRejectsChangedOrAmbiguousState(t *testing.T) {
	for _, scenario := range []string{"operation_changed", "original_archived", "target_deleted", "target_attached", "target_reserved", "version_taken", "competing_operation", "backup_blocked", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			store, observed := conflictingHistoricalVersionFixture(t)
			if err := store.mutate(t.Context(), func(s *State) error {
				switch scenario {
				case "operation_changed":
					op := s.PendingOperations[observed.ID]
					op.ExpectedGeneration++
					s.PendingOperations[op.ID] = op
				case "original_archived":
					setLifecycle(s, "original", Archived)
				case "target_deleted":
					setLifecycle(s, "reserved", Deleted)
				case "target_attached":
					w := s.Workspaces["global"]
					w.SessionIDs = append(w.SessionIDs, "reserved")
					s.Workspaces[w.ID] = w
				case "target_reserved":
					s.PendingCreates["reserved"] = PendingCreate{OperationID: "other", SessionID: "reserved", WorkspaceID: "global"}
				case "version_taken":
					m := *observed.Mapping
					m.SourceKey, m.SessionID = "source:review:new", "original"
					s.SourceMappings[m.SourceKey] = m
				case "competing_operation":
					op := observed
					op.ID = "another-operation"
					s.PendingOperations[op.ID] = op
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if scenario == "backup_blocked" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(store.Path()), "historical-version-backups"), []byte("block directory"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "cancelled" {
				cancel()
			}
			before, _ := os.ReadFile(store.Path())
			err := store.CommitHistoricalVersion(ctx, observed, "source:review:new")
			if err == nil || (scenario != "backup_blocked" && scenario != "cancelled" && !errors.Is(err, ErrMutationConflict)) {
				t.Fatalf("unexpected result: %v", err)
			}
			after, _ := os.ReadFile(store.Path())
			if !bytes.Equal(before, after) {
				t.Fatal("rejected repair changed registry")
			}
		})
	}
}
