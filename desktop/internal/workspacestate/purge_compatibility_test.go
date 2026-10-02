package workspacestate

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"

	previous "reasonix/desktop/internal/workspacestate/testdata/v2previous"
)

func TestPurgeV2UpgradeRejectsPreviousWriterWithoutLosingEvidence(t *testing.T) {
	for _, phase := range []string{"prepared", "tombstoned", "content_removed", "committed"} {
		t.Run(phase, func(t *testing.T) {
			store, expected := seedArchivedProcessState(t)
			legacy, err := store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			legacy.Version = 2
			legacyBody, err := json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.Path(), legacyBody, 0o600); err != nil {
				t.Fatal(err)
			}
			old := previous.NewStore(store.Path())
			// Produce a real legacy prepare using the previous implementation.
			if err := old.BeginPurge(t.Context(), "victim", expected); err != nil {
				t.Fatal(err)
			}
			if err := store.mutate(t.Context(), func(s *State) error {
				op := s.PendingOperations["purge-victim"]
				op.extra = map[string]json.RawMessage{"future": json.RawMessage(`{"nested":[1,2]}`)}
				s.PendingOperations[op.ID] = op
				s.extra = map[string]json.RawMessage{"futureRoot": json.RawMessage(`{"keep":true}`)}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			state, err := store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if phase != "prepared" {
				if err := store.ResumePurge(t.Context(), "victim", state.PendingOperations["purge-victim"]); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "content_removed" || phase == "committed" {
				if err := store.AdvancePurge(t.Context(), "victim", "content_removed"); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "committed" {
				if err := store.CompletePurge(t.Context(), "victim"); err != nil {
					t.Fatal(err)
				}
			}
			state, err = store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(state.PendingOperations["purge-victim"])
			if state.PendingOperations["purge-victim"].Phase != phase {
				t.Fatalf("upgraded reader lost purge phase %s", phase)
			}
			before, err := os.ReadFile(store.Path())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := old.Load(t.Context()); !errors.Is(err, previous.ErrUnsupportedVersion) {
				t.Fatalf("previous reader must reject v3: %v", err)
			}
			if err := old.RenameWorkspace(t.Context(), GlobalWorkspaceID, "Old writer"); !errors.Is(err, previous.ErrUnsupportedVersion) {
				t.Fatalf("previous writer must reject v3: %v", err)
			}
			after, err := os.ReadFile(store.Path())
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected previous writer modified v3 registry")
			}
			if err := store.RenameWorkspace(t.Context(), GlobalWorkspaceID, "Unrelated title"); err != nil {
				t.Fatal(err)
			}
			state, err = store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(state.PendingOperations["purge-victim"])
			if string(got) != string(want) || string(state.extra["futureRoot"]) != `{"keep":true}` {
				t.Fatalf("upgraded writer dropped evidence: %s => %s", want, got)
			}
			if phase == "committed" {
				before, _ := os.ReadFile(store.Path())
				if err := store.CompletePurge(t.Context(), "victim"); err != nil {
					t.Fatal(err)
				}
				after, _ := os.ReadFile(store.Path())
				if string(before) != string(after) {
					t.Fatal("idempotent completion rewrote registry")
				}
			}
		})
	}
}
