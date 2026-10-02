package control

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"reasonix/internal/event"
)

func TestPublishedRuntimeStateDoesNotWaitForSamplingLocks(t *testing.T) {
	isolateControlConfigHome(t)
	c := newOwnedTestController(t, Options{SessionDir: t.TempDir()})
	defer c.Close()
	want := c.RuntimeStateSnapshot()
	if want.SchemaVersion != 1 || want.Revision == 0 {
		t.Fatalf("initial state was not committed: %+v", want)
	}
	// A producer waiting on a controller/session owner holds the sampling
	// mutex. Neither that mutex nor the controller lock may gate UI reads.
	c.runtimeState.mu.Lock()
	defer c.runtimeState.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	done := make(chan event.RuntimeStateSnapshot, 1)
	go func() { done <- c.PublishedRuntimeStateSnapshot() }()
	select {
	case got := <-done:
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("committed state changed while producer is blocked: got=%+v want=%+v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("published read waited on an owner or sampling lock")
	}
}

func TestPublishedRuntimeStateTracksCancellationAndCompletion(t *testing.T) {
	isolateControlConfigHome(t)
	sink := &runtimeStateTestSink{Sink: event.FuncSink(func(event.Event) {}), states: make(chan event.RuntimeStateSnapshot, 64)}
	c := newOwnedTestController(t, Options{SessionDir: t.TempDir(), Sink: sink})
	defer c.Close()
	release := make(chan struct{})
	releaseBody := sync.OnceFunc(func() { close(release) })
	defer releaseBody()
	started := c.PublishedRuntimeStateSnapshot()
	c.runGuarded(func(ctx context.Context) error { <-release; return ctx.Err() })
	for _, phase := range []string{"executing", "cancelling", "idle"} {
		switch phase {
		case "cancelling":
			c.CancelSession()
		case "idle":
			releaseBody()
		}
		published := runtimeStateAwait(t, sink.states, func(s event.RuntimeStateSnapshot) bool { return s.Phase == phase })
		got := c.PublishedRuntimeStateSnapshot()
		if got.Phase != phase || got.Revision < published.Revision || got.RuntimeEpoch != started.RuntimeEpoch {
			t.Fatalf("committed state did not follow %s: %+v", phase, got)
		}
		if phase == "idle" && got.ActiveWork() {
			t.Fatalf("completed turn still appears active: %+v", got)
		}
	}
}

func TestPublishedRuntimeStateOwnsMutableObservations(t *testing.T) {
	c := &Controller{}
	state := event.RuntimeStateSnapshot{
		Revision: 1, Todos: []event.Todo{{Content: "original"}},
		Interactions: []event.PendingInteraction{{RequestID: "original"}},
		Recovery:     &event.RecoveryStatus{Reason: "original"},
		Maintenance:  &event.MaintenanceState{OperationID: "original"},
	}
	c.runtimeState.commitSnapshot(state)
	// Neither producer-owned input nor a UI consumer may rewrite a version
	// another reader has already observed.
	state.Todos[0].Content = "producer mutation"
	first := c.PublishedRuntimeStateSnapshot()
	if first.Todos[0].Content != "original" {
		t.Fatal("producer retained ownership of committed todos")
	}
	first.Todos[0].Content = "consumer mutation"
	first.Interactions[0].RequestID = "consumer mutation"
	first.Recovery.Reason = "consumer mutation"
	first.Maintenance.OperationID = "consumer mutation"
	second := c.PublishedRuntimeStateSnapshot()
	if second.Todos[0].Content != "original" || second.Interactions[0].RequestID != "original" ||
		second.Recovery.Reason != "original" || second.Maintenance.OperationID != "original" {
		t.Fatalf("a reader mutated the committed version: %+v", second)
	}
}
