package control

import (
	"context"
	"sync"
	"testing"
	"time"

	"reasonix/internal/event"
)

type approvalBlockingRunner struct {
	c *Controller
}

func (r *approvalBlockingRunner) Run(ctx context.Context, _ string) error {
	_, _, err := gateApprover{c: r.c}.Approve(ctx, "bash", "go test ./...", nil)
	return err
}

type askBlockingRunner struct {
	c *Controller
}

func (r *askBlockingRunner) Run(ctx context.Context, _ string) error {
	_, err := r.c.Ask(ctx, []event.AskQuestion{{
		ID:      "choice",
		Prompt:  "Pick one",
		Options: []event.AskOption{{Label: "A"}, {Label: "B"}},
	}})
	return err
}

func TestCancelClearsPendingApprovalRuntimeStatus(t *testing.T) {
	approvals := make(chan event.Approval, 1)
	done := make(chan event.Event, 1)
	c := newOwnedTestController(t, Options{Sink: event.FuncSink(func(e event.Event) {
		switch e.Kind {
		case event.ApprovalRequest:
			approvals <- e.Approval
		case event.TurnDone:
			done <- e
		}
	})})
	runner := &approvalBlockingRunner{c: c}
	c.runner = runner

	c.Send("needs approval")
	select {
	case <-approvals:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for approval request")
	}
	if st := c.RuntimeStatus(); !st.Running || !st.PendingPrompt || !st.Cancellable || st.CancelRequested {
		t.Fatalf("status before cancel = %+v, want running pending cancellable", st)
	}

	c.Cancel()
	c.Cancel()
	assertCancelClearedPendingRuntimeStatus(t, c.RuntimeStatus())
	if e := waitTurnDoneEvent(t, done); !e.Cancelled {
		t.Fatal("cancelled turn_done event was not marked as user-cancelled")
	}
	// TurnDone is emitted inside the finishing window; Running() (and the
	// RuntimeStatus it feeds) stays true until finishGuardedTurn's deferred
	// clear runs. Wait for the gate to reopen before asserting idle.
	waitIdle(t, c)
	if st := c.RuntimeStatus(); st.Running || st.PendingPrompt || st.Cancellable || st.CancelRequested {
		t.Fatalf("status after turn done = %+v, want idle", st)
	}
}

func TestCancelClearsPendingAskRuntimeStatus(t *testing.T) {
	asks := make(chan event.Ask, 1)
	done := make(chan event.Event, 1)
	c := newOwnedTestController(t, Options{Sink: event.FuncSink(func(e event.Event) {
		switch e.Kind {
		case event.AskRequest:
			asks <- e.Ask
		case event.TurnDone:
			done <- e
		}
	})})
	runner := &askBlockingRunner{c: c}
	c.runner = runner

	c.Send("ask user")
	select {
	case <-asks:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for ask request")
	}
	if st := c.RuntimeStatus(); !st.Running || !st.PendingPrompt || !st.Cancellable || st.CancelRequested {
		t.Fatalf("status before cancel = %+v, want running pending cancellable", st)
	}

	c.Cancel()
	assertCancelClearedPendingRuntimeStatus(t, c.RuntimeStatus())
	waitTurnDoneEvent(t, done)
	// TurnDone is emitted inside the finishing window; Running() (and the
	// RuntimeStatus it feeds) stays true until finishGuardedTurn's deferred
	// clear runs. Wait for the gate to reopen before asserting idle.
	waitIdle(t, c)
	if st := c.RuntimeStatus(); st.Running || st.PendingPrompt || st.Cancellable || st.CancelRequested {
		t.Fatalf("status after turn done = %+v, want idle", st)
	}
}

func TestCloseCancelsPendingAskRuntimeStatus(t *testing.T) {
	asks := make(chan event.Ask, 1)
	done := make(chan event.Event, 1)
	c := newOwnedTestController(t, Options{Sink: event.FuncSink(func(e event.Event) {
		switch e.Kind {
		case event.AskRequest:
			asks <- e.Ask
		case event.TurnDone:
			done <- e
		}
	})})
	c.runner = &askBlockingRunner{c: c}

	c.Send("ask user")
	select {
	case <-asks:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ask request")
	}

	c.Close()
	select {
	case e := <-done:
		if !e.Cancelled {
			t.Fatal("closed turn_done event was not marked as cancelled")
		}
	case <-time.After(time.Second):
		c.Cancel()
		t.Fatal("Close did not cancel the pending ask waiter")
	}
	waitIdle(t, c)
	if st := c.RuntimeStatus(); st.Running || st.PendingPrompt || st.Cancellable || st.CancelRequested {
		t.Fatalf("status after Close = %+v, want idle", st)
	}
}

func TestCloseDoesNotResurrectFinishingState(t *testing.T) {
	turnStarted := make(chan struct{})
	turnDoneEntered := make(chan struct{}, 1)
	releaseTurnDone := make(chan struct{})
	c := newOwnedTestController(t, Options{Sink: holdFinishingWindow(releaseTurnDone, turnDoneEntered, nil)})

	c.runGuarded(func(ctx context.Context) error {
		close(turnStarted)
		<-ctx.Done()
		return ctx.Err()
	})
	<-turnStarted

	c.Close()
	select {
	case <-turnDoneEntered:
	case <-time.After(time.Second):
		c.Cancel()
		t.Fatal("Close did not cancel the active turn")
	}
	defer close(releaseTurnDone)

	if st := c.RuntimeStatus(); st.Running || st.PendingPrompt || st.Cancellable || st.CancelRequested {
		t.Fatalf("closed controller resurrected active state during TurnDone delivery: %+v", st)
	}
}

func TestTurnFinishingDoneClosesAfterTurnDoneFanout(t *testing.T) {
	turnDoneEntered := make(chan struct{}, 1)
	releaseTurnDone := make(chan struct{})
	c := newOwnedTestController(t, Options{Sink: holdFinishingWindow(releaseTurnDone, turnDoneEntered, nil)})
	t.Cleanup(c.Close)

	c.runGuarded(func(context.Context) error { return nil })
	select {
	case <-turnDoneEntered:
	case <-time.After(time.Second):
		t.Fatal("TurnDone delivery did not enter the finishing window")
	}

	done, ok := c.TurnFinishingDone()
	if !ok || done == nil {
		close(releaseTurnDone)
		t.Fatal("controller did not expose its active finishing boundary")
	}
	select {
	case <-done:
		close(releaseTurnDone)
		t.Fatal("finishing boundary closed before TurnDone fan-out returned")
	default:
	}

	close(releaseTurnDone)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("finishing boundary did not close after TurnDone fan-out")
	}
	if _, ok := c.TurnFinishingDone(); ok {
		t.Fatal("controller retained a stale finishing boundary")
	}
}

func TestTurnIdleDoneCoversExecutionAndTurnDoneFanout(t *testing.T) {
	turnStarted := make(chan struct{})
	releaseTurn := make(chan struct{})
	turnDoneEntered := make(chan struct{}, 1)
	releaseTurnDone := make(chan struct{})
	c := newOwnedTestController(t, Options{Sink: holdFinishingWindow(releaseTurnDone, turnDoneEntered, nil)})
	t.Cleanup(c.Close)

	c.runGuarded(func(context.Context) error {
		close(turnStarted)
		<-releaseTurn
		return nil
	})
	select {
	case <-turnStarted:
	case <-time.After(time.Second):
		close(releaseTurn)
		t.Fatal("turn did not start")
	}

	done, ok := c.TurnIdleDone()
	if !ok || done == nil {
		close(releaseTurn)
		t.Fatal("controller did not expose its active idle boundary")
	}
	close(releaseTurn)
	select {
	case <-turnDoneEntered:
	case <-time.After(time.Second):
		t.Fatal("TurnDone delivery did not enter the finishing window")
	}
	select {
	case <-done:
		close(releaseTurnDone)
		t.Fatal("idle boundary closed before TurnDone fan-out returned")
	default:
	}

	close(releaseTurnDone)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle boundary did not close after TurnDone fan-out")
	}
	if _, ok := c.TurnIdleDone(); ok {
		t.Fatal("controller retained a stale idle boundary")
	}
}

func TestTurnIdleDoneStaysOpenAcrossParkedTurn(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstTurnDoneEntered := make(chan struct{})
	releaseFirstTurnDone := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	var firstTurnDone sync.Once
	c := newOwnedTestController(t, Options{Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			firstTurnDone.Do(func() {
				close(firstTurnDoneEntered)
				<-releaseFirstTurnDone
			})
		}
	})})
	t.Cleanup(c.Close)

	c.runGuarded(func(context.Context) error {
		close(firstStarted)
		<-releaseFirst
		return nil
	})
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		close(releaseFirst)
		t.Fatal("first turn did not start")
	}
	done, ok := c.TurnIdleDone()
	if !ok || done == nil {
		close(releaseFirst)
		t.Fatal("controller did not expose the first turn's idle boundary")
	}

	close(releaseFirst)
	select {
	case <-firstTurnDoneEntered:
	case <-time.After(time.Second):
		close(releaseFirstTurnDone)
		t.Fatal("first TurnDone did not enter the finishing window")
	}
	if got := c.runGuarded(func(context.Context) error {
		close(secondStarted)
		<-releaseSecond
		return nil
	}); got != turnParked {
		close(releaseFirstTurnDone)
		t.Fatalf("turn admitted during finishing = %v, want parked", got)
	}
	close(releaseFirstTurnDone)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		close(releaseSecond)
		t.Fatal("parked turn did not start after finishing completed")
	}
	select {
	case <-done:
		close(releaseSecond)
		t.Fatal("idle boundary closed between the finishing and parked turns")
	default:
	}

	close(releaseSecond)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle boundary did not close after the parked turn completed")
	}
}

func assertCancelClearedPendingRuntimeStatus(t *testing.T, st RuntimeStatus) {
	t.Helper()
	if st.PendingPrompt {
		t.Fatalf("status immediately after cancel = %+v, want pending prompt cleared", st)
	}
	if st.Running {
		if !st.Cancellable || !st.CancelRequested {
			t.Fatalf("status immediately after cancel = %+v, want running cancelling without pending prompt", st)
		}
		return
	}
	if st.Cancellable || st.CancelRequested {
		t.Fatalf("status immediately after cancel = %+v, want idle when turn already completed", st)
	}
}

func waitTurnDoneEvent(t *testing.T, done <-chan event.Event) event.Event {
	t.Helper()
	select {
	case e := <-done:
		if e.Kind != event.TurnDone {
			t.Fatalf("event = %v, want TurnDone", e.Kind)
		}
		return e
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for turn_done")
	}
	return event.Event{}
}
