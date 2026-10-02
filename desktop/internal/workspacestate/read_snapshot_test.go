package workspacestate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadSnapshotOverlappingValidationSharesFlightAndCancellation(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "registry.json"))
	writeReadSnapshotFixture(t, store.Path(), newState())
	type result struct {
		snapshot *ReadSnapshot
		err      error
	}
	results := make(chan result, 8)
	canceled, cancel := context.WithCancel(t.Context())
	defer cancel()
	func() {
		// Hold the authoritative read boundary so all callers deterministically
		// join the same verification before any file can be read.
		store.mu.Lock()
		defer store.mu.Unlock()
		for i := range 8 {
			ctx := t.Context()
			if i == 0 {
				ctx = canceled
			}
			go func() {
				snapshot, err := store.VerifySnapshot(ctx)
				results <- result{snapshot, err}
			}()
		}
		for {
			store.verificationMu.Lock()
			joined := store.verification != nil && store.verification.readers == 8
			store.verificationMu.Unlock()
			if joined {
				break
			}
			if err := t.Context().Err(); err != nil {
				t.Fatal(err)
			}
			runtime.Gosched()
		}
		cancel()
	}()
	var shared *ReadSnapshot
	cancellations := 0
	for range 8 {
		r := <-results
		if errors.Is(r.err, context.Canceled) {
			cancellations++
			continue
		}
		if r.err != nil {
			t.Fatal(r.err)
		}
		if shared != nil && shared != r.snapshot {
			t.Fatal("overlapping validation did not share a snapshot")
		}
		shared = r.snapshot
	}
	if cancellations != 1 || shared == nil {
		t.Fatalf("cancellations=%d snapshot=%v", cancellations, shared)
	}
}

func writeReadSnapshotFixture(t testing.TB, path string, state State) {
	t.Helper()
	normalize(&state)
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReadSnapshotKeepsIdentityAndDetectsLegacyReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	state := newState()
	state.WorkspaceIDs = []string{"a"}
	state.Workspaces["a"] = Workspace{ID: "a", Root: "/a", SessionIDs: []string{"s"}}
	state.Presentation["s"] = Presentation{TopicID: "topic", Title: "before"}
	writeReadSnapshotFixture(t, path, state)
	store := NewStore(path)
	first, err := store.VerifySnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.VerifySnapshot(t.Context())
	if err != nil || first != second {
		t.Fatalf("unchanged bytes rebuilt snapshot: %v", err)
	}
	// Rendering a previously published view must not wait for the mutex held
	// by disk verification or a durable mutation.
	store.mu.Lock()
	display := store.PublishedSnapshot()
	store.mu.Unlock()
	if display != first {
		t.Fatal("published display snapshot changed without a publication")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	state.Workspaces["a"] = Workspace{ID: "a", Root: "/b", SessionIDs: []string{"s"}}
	writeReadSnapshotFixture(t, path, state) // Same generation and file length.
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if store.PublishedSnapshot() != first {
		t.Fatal("display read unexpectedly performs external validation")
	}
	current, err := store.VerifySnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if current == first || current.Session("s").Workspace.Root != "/b" || first.Session("s").Workspace.Root != "/a" {
		t.Fatal("legacy replacement was missed or changed a retained snapshot")
	}
	if got := current.Session("s"); len(got.Workspace.SessionIDs) != 0 || got.Workspace.Organization != nil || !got.Registered {
		t.Fatalf("lookup must be bounded metadata: %+v", got)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifySnapshot(t.Context()); err == nil {
		t.Fatal("corrupt authority accepted")
	}
	if store.PublishedSnapshot() != current {
		t.Fatal("failed verification replaced display snapshot")
	}
	if err := store.RenameWorkspace(t.Context(), "a", "unsafe"); err == nil {
		t.Fatal("mutation overwrote corrupt authority")
	}
}

func TestReadSnapshotCommitPublicationAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	state := newState()
	state.WorkspaceIDs = []string{"a"}
	state.Workspaces["a"] = Workspace{ID: "a", Title: "old", SessionIDs: []string{"s", "other"}}
	state.Presentation["s"], state.Presentation["other"] = Presentation{TopicID: "topic"}, Presentation{TopicID: "topic"}
	writeReadSnapshotFixture(t, path, state)
	store := NewStore(path)
	first, err := store.VerifySnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Session("s").SharedTopic {
		t.Fatal("shared active topic missing")
	}
	if err := store.RenameWorkspace(t.Context(), "a", "new"); err != nil {
		t.Fatal(err)
	}
	current := store.PublishedSnapshot()
	if current == first || current.Session("s").Workspace.Title != "new" || first.Session("s").Workspace.Title != "old" {
		t.Fatal("durable commit did not publish independently owned snapshot")
	}
	verified, err := store.VerifySnapshot(t.Context())
	if err != nil || verified != current {
		t.Fatalf("committed bytes rebuilt snapshot: %v", err)
	}
	state.SessionStates["other"] = SessionState{Lifecycle: Archived}
	writeReadSnapshotFixture(t, path, state)
	archived, err := store.VerifySnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if archived.Session("s").SharedTopic {
		t.Fatal("archived member counted as a shared active topic")
	}
	// Reject an externally persisted ownership conflict before publication.
	state.WorkspaceIDs = append(state.WorkspaceIDs, "b")
	state.Workspaces["b"] = Workspace{ID: "b", SessionIDs: []string{"s"}}
	writeReadSnapshotFixture(t, path, state)
	if _, err := store.VerifySnapshot(t.Context()); err == nil || store.PublishedSnapshot() != archived {
		t.Fatal("conflicting membership replaced the verified snapshot")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	empty, err := store.VerifySnapshot(t.Context())
	if err != nil || empty.Session("s").Registered {
		t.Fatalf("removed registry retained authority: %v", err)
	}
}

func BenchmarkPublishedSessionLookup(b *testing.B) {
	for _, count := range []int{100, 10_000, 100_000} {
		for _, projects := range []int{1, 100, 1000} {
			b.Run(fmt.Sprintf("sessions=%d/projects=%d", count, projects), func(b *testing.B) {
				state := newState()
				for i := range projects {
					id := fmt.Sprintf("p%d", i)
					state.WorkspaceIDs = append(state.WorkspaceIDs, id)
					state.Workspaces[id] = Workspace{ID: id}
				}
				for i := range count {
					id, project := fmt.Sprintf("s%d", i), fmt.Sprintf("p%d", i%projects)
					w := state.Workspaces[project]
					w.SessionIDs = append(w.SessionIDs, id)
					state.Workspaces[project] = w
					state.SessionStates[id] = SessionState{Lifecycle: Active}
				}
				store := NewStore(filepath.Join(b.TempDir(), "registry.json"))
				store.publishSnapshotLocked(nil, state)
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if !store.PublishedSnapshot().Session("s0").Registered {
						b.Fatal("missing session")
					}
				}
			})
		}
	}
}

func TestReadSnapshotDoesNotRetainMutableTransactionInputs(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "registry.json"))
	members := []string{"s"}
	if err := store.mutate(t.Context(), func(state *State) error {
		state.WorkspaceIDs = []string{"a"}
		state.Workspaces["a"] = Workspace{ID: "a", SessionIDs: members}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := store.PublishedSnapshot()
	members[0] = "replaced"
	if snapshot.Session("s").Workspace.ID != "a" || snapshot.Session("replaced").Workspace.ID != "" {
		t.Fatal("transaction input mutated the published snapshot")
	}
	loaded, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	w := loaded.Workspaces["a"]
	w.SessionIDs[0] = "another"
	loaded.Presentation["s"] = Presentation{Title: "changed"}
	if snapshot.Session("s").Presentation.Title != "" {
		t.Fatal("compatibility Load leaked the snapshot's maps")
	}
}
