package control

import (
	"context"
	"sync"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/session"
)

type synchronousContextKey struct{}

func TestSynchronousTurnPreservesCallerContext(t *testing.T) {
	c := newOwnedTestController(t, Options{Sink: event.Discard})
	t.Cleanup(c.Close)
	deadline := time.Now().Add(time.Minute).Round(0)
	parent, cancel := context.WithDeadline(context.WithValue(t.Context(), synchronousContextKey{}, "caller-value"), deadline)
	defer cancel()

	err := c.runSynchronousTurn(parent, nil, func(ctx context.Context) error {
		if got := ctx.Value(synchronousContextKey{}); got != "caller-value" {
			t.Fatalf("caller context value = %v, want caller-value", got)
		}
		gotDeadline, ok := ctx.Deadline()
		if !ok || !gotDeadline.Equal(deadline) {
			t.Fatalf("caller deadline = %v, %v; want %v, true", gotDeadline, ok, deadline)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSynchronousCloseKeepsExecutionBoundUntilTerminalCommit(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	c, service, runtime := exclusiveTestController(t, holdFinishingWindow(release, entered, nil))
	done := make(chan error, 1)
	finished := make(chan struct{})
	releaseTerminal := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() {
		releaseTerminal()
		<-finished
	})
	go func() {
		defer close(finished)
		done <- c.runSynchronousTurn(t.Context(), nil, func(context.Context) error { return nil })
	}()
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal("synchronous terminal publication did not start")
	}
	c.Close()
	if phase := runtime.StateSnapshot().Phase; phase != session.RuntimeFinalizing {
		t.Fatalf("runtime phase during terminal barrier = %s, want finalizing", phase)
	}
	if !runtime.OwnsExecution(c.ExecutionGeneration()) {
		t.Fatal("close released synchronous execution owner before terminal commit")
	}
	if _, ok := service.Runtime(runtime.Ref()); !ok {
		t.Fatal("close retired synchronous runtime before terminal commit")
	}
	releaseTerminal()
	// The barrier proves execution ownership ordering. Real session teardown
	// flushes and closes its writer; its speed is not a one-second contract.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("synchronous turn: %v", err)
		}
	case <-t.Context().Done():
		t.Fatal("synchronous turn did not finish")
	}
	if runtime.OwnsExecution(c.ExecutionGeneration()) {
		t.Fatal("closed synchronous controller retained execution owner")
	}
}
