package legacycleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/filelock"
)

func TestStoreFreezesInitialCandidateSetAndRoundTripsUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup.json")
	store := New(path)
	initial := State{BatchID: "batch", Items: map[string]Candidate{
		"session:a": {ID: "session:a", Kind: "session", SessionID: "a", OperationID: "cleanup-a", Phase: "registered"},
	}}
	state, created, err := store.Initialize(context.Background(), initial)
	if err != nil || !created || len(state.Items) != 1 {
		t.Fatalf("Initialize = (%+v, %v, %v)", state, created, err)
	}
	second := State{BatchID: "other", Items: map[string]Candidate{
		"session:b": {ID: "session:b", Kind: "session", SessionID: "b", OperationID: "cleanup-b", Phase: "registered"},
	}}
	state, created, err = store.Initialize(context.Background(), second)
	if err != nil || created || state.BatchID != "batch" || state.Items["session:b"].ID != "" {
		t.Fatalf("second Initialize changed frozen set: (%+v, %v, %v)", state, created, err)
	}
	state, err = store.Update(context.Background(), func(next *State) error {
		item := next.Items["session:a"]
		item.Phase, item.Classification = "archived", "empty"
		next.Items[item.ID] = item
		return nil
	})
	if err != nil || state.Items["session:a"].Phase != "archived" {
		t.Fatalf("Update = (%+v, %v)", state, err)
	}
}

func TestStoreRejectsFutureAndCorruptStateWithoutReplacingIt(t *testing.T) {
	for _, fixture := range []string{`{"version":2,"batchId":"future"}`, `{not-json`} {
		path := filepath.Join(t.TempDir(), "cleanup.json")
		if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
			t.Fatal(err)
		}
		store := New(path)
		_, _, err := store.Initialize(context.Background(), State{BatchID: "new", Items: map[string]Candidate{}})
		if !errors.Is(err, ErrUnsupportedVersion) && !errors.Is(err, ErrCorruptState) {
			t.Fatalf("Initialize error = %v", err)
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil || string(body) != fixture {
			t.Fatalf("unknown state was modified: %q, %v", body, readErr)
		}
	}
}

func TestStoreLoadFromFreshParentRemainsUninitialized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "desktop", "cleanup.json")
	store := New(path)
	if _, err := store.Load(context.Background()); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("Load error = %v, want ErrNotInitialized", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("fresh read created state file: %v", err)
	}
}

func TestStoreWorkerLockIsExclusiveAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup.json")
	first := New(path)
	if _, _, err := first.Initialize(context.Background(), State{BatchID: "batch", Items: map[string]Candidate{}}); err != nil {
		t.Fatal(err)
	}
	release, err := first.TryAcquireWorker()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := New(path).TryAcquireWorker(); !errors.Is(err, filelock.ErrHeld) {
		t.Fatalf("second worker lock error = %v, want ErrHeld", err)
	}
}

func TestStoreTransitionSerializesOtherInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup.json")
	first, second := New(path), New(path)
	if _, _, err := first.Initialize(t.Context(), State{BatchID: "batch", Items: map[string]Candidate{
		"topic:a": {ID: "topic:a", Kind: "topic", TopicID: "a", OperationID: "cleanup-a", Phase: "registered"},
	}}); err != nil {
		t.Fatal(err)
	}
	effectStarted := make(chan struct{})
	releaseEffect := make(chan struct{})
	transitionDone := make(chan error, 1)
	go func() {
		_, err := first.Transition(context.Background(), func(state *State) error {
			item := state.Items["topic:a"]
			item.Phase = "archive_pending"
			state.Items[item.ID] = item
			return nil
		}, func() error {
			close(effectStarted)
			<-releaseEffect
			return nil
		}, func(state *State, _ error) error {
			item := state.Items["topic:a"]
			item.Phase = "archived"
			state.Items[item.ID] = item
			return nil
		})
		transitionDone <- err
	}()
	<-effectStarted
	if release, err := filelock.TryAcquire(path + ".lock"); !errors.Is(err, filelock.ErrHeld) {
		if release != nil {
			release()
		}
		t.Fatalf("transition did not hold cross-process state lock: %v", err)
	}
	close(releaseEffect)
	if err := <-transitionDone; err != nil {
		t.Fatal(err)
	}
	if _, err := second.Update(t.Context(), func(state *State) error {
		item := state.Items["topic:a"]
		item.Restored = true
		state.Items[item.ID] = item
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, err := first.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Items["topic:a"].Phase != "archived" || !state.Items["topic:a"].Restored {
		t.Fatalf("serialized state = %+v", state.Items["topic:a"])
	}
}
