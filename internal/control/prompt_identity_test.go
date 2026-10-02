package control

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/session"
)

func TestResolvePromptExactRejectsStaleTurnBeforeDispatch(t *testing.T) {
	c := newOwnedTestController(t, Options{})
	t.Cleanup(c.Close)
	err := c.ResolvePromptExact(PromptIdentity{
		PromptID: "prompt-1", TurnID: "turn-stale", Kind: PromptAsk,
	}, PromptAnswer{})
	if !errors.Is(err, ErrPromptStaleTurn) {
		t.Fatalf("ResolvePromptExact error = %v, want ErrPromptStaleTurn", err)
	}
}

func TestResolvePromptExactRejectsIncompleteIdentity(t *testing.T) {
	c := newOwnedTestController(t, Options{})
	t.Cleanup(c.Close)
	err := c.ResolvePromptExact(PromptIdentity{PromptID: "prompt-1", Kind: PromptAsk}, PromptAnswer{})
	if !errors.Is(err, ErrPromptNotPending) {
		t.Fatalf("ResolvePromptExact error = %v, want ErrPromptNotPending", err)
	}
}

func TestResolvePromptExactRejectsStaleRuntime(t *testing.T) {
	c := newOwnedTestController(t, Options{})
	t.Cleanup(c.Close)
	c.SetTurnEventRoutingMetadata("runtime-current", "")
	err := c.ResolvePromptExact(PromptIdentity{
		PromptID: "prompt-1", TurnID: "turn-any", RuntimeEpoch: "runtime-old", Kind: PromptAsk,
	}, PromptAnswer{})
	if !errors.Is(err, ErrPromptStaleRuntime) {
		t.Fatalf("ResolvePromptExact error = %v, want ErrPromptStaleRuntime", err)
	}
}

func TestResolvePromptExactRejectsLegacyIdentityAfterRuntimeEpochIsSet(t *testing.T) {
	c := newOwnedTestController(t, Options{})
	t.Cleanup(c.Close)
	c.SetTurnEventRoutingMetadata("runtime-current", "")
	err := c.ResolvePromptExact(PromptIdentity{PromptID: "legacy", TurnID: "turn-any", Kind: PromptAsk}, PromptAnswer{})
	if !errors.Is(err, ErrPromptStaleRuntime) {
		t.Fatalf("legacy exact resolve error = %v, want ErrPromptStaleRuntime", err)
	}
}

func TestResolvePromptExactRejectsClosedController(t *testing.T) {
	c := newOwnedTestController(t, Options{})
	c.Close()
	err := c.ResolvePromptExact(PromptIdentity{PromptID: "p", TurnID: "t", Kind: PromptAsk}, PromptAnswer{})
	if !errors.Is(err, ErrPromptNotPending) {
		t.Fatalf("closed resolver error = %v", err)
	}
}

func TestPendingPromptOwnerTracksResolvedIdentity(t *testing.T) {
	var owner PendingPromptOwner
	id := PromptIdentity{PromptID: "p", TurnID: "t", Kind: PromptAsk}
	if err := owner.Register(id); err != nil {
		t.Fatal(err)
	}
	if got, ok := owner.Identity("p"); !ok || got != id {
		t.Fatalf("registered identity = %+v, %v", got, ok)
	}
	owner.MarkResolved(id)
	if _, ok := owner.Identity("p"); ok {
		t.Fatal("resolved prompt remains pending")
	}
	if !owner.WasResolved("p") {
		t.Fatal("resolved prompt was not recorded")
	}
}

func TestPendingPromptOwnerRejectsConcurrentResolveReservation(t *testing.T) {
	var owner PendingPromptOwner
	id := PromptIdentity{PromptID: "p", TurnID: "t", Kind: PromptMCP}
	if err := owner.Register(id); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { <-start; results <- owner.BeginResolve(id) })
	}
	close(start)
	wg.Wait()
	close(results)
	var success, already int
	for err := range results {
		if err == nil {
			success++
		}
		if errors.Is(err, ErrPromptAlreadyResolved) {
			already++
		}
	}
	if success != 1 || already != 1 {
		t.Fatalf("resolve reservations = success %d already %d", success, already)
	}
}

func TestPendingPromptOwnerResolveFailureBecomesUnavailable(t *testing.T) {
	var owner PendingPromptOwner
	id := PromptIdentity{PromptID: "p-fail", TurnID: "t", Kind: PromptAsk}
	if err := owner.RegisterPrompt(PendingPrompt{Identity: id, Resolve: func(PromptAnswer) error { return errors.New("persist failed") }}); err != nil {
		t.Fatal(err)
	}
	if err := owner.Resolve(id, PromptAnswer{}); !errors.Is(err, ErrPromptUnavailable) || !strings.Contains(err.Error(), "persist failed") {
		t.Fatalf("resolve error = %v", err)
	}
	if _, ok := owner.Identity(id.PromptID); ok {
		t.Fatal("failed answerer remained pending")
	}
	resolution, ok := owner.Resolution(id.PromptID)
	if !ok || resolution.State != PromptUnavailable {
		t.Fatalf("failed answerer resolution = %+v %v", resolution, ok)
	}
}

func TestPendingPromptOwnerTerminatesUnavailableAnswerer(t *testing.T) {
	var owner PendingPromptOwner
	id := PromptIdentity{PromptID: "p-unavailable", TurnID: "t", Kind: PromptAsk}
	if err := owner.Register(id); err != nil {
		t.Fatal(err)
	}
	if err := owner.Resolve(id, PromptAnswer{}); !errors.Is(err, ErrPromptUnavailable) {
		t.Fatalf("resolve error = %v, want ErrPromptUnavailable", err)
	}
	if _, ok := owner.Identity(id.PromptID); ok {
		t.Fatal("unavailable prompt remains pending")
	}
	resolution, ok := owner.Resolution(id.PromptID)
	if !ok || resolution.State != PromptUnavailable {
		t.Fatalf("resolution = %+v, %v", resolution, ok)
	}
}

func TestPendingPromptOwnerCancellationDoesNotWaitForAnswerer(t *testing.T) {
	var owner PendingPromptOwner
	id := PromptIdentity{PromptID: "p-blocked", TurnID: "t", Kind: PromptAsk}
	answerStarted := make(chan struct{})
	releaseAnswer := make(chan struct{})
	if err := owner.RegisterPrompt(PendingPrompt{Identity: id, Resolve: func(PromptAnswer) error {
		close(answerStarted)
		<-releaseAnswer
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	resolved := make(chan error, 1)
	go func() { resolved <- owner.Resolve(id, PromptAnswer{}) }()
	<-answerStarted
	cancelled := make(chan struct{})
	go func() {
		owner.CancelAll()
		close(cancelled)
	}()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for the blocked answerer")
	}
	close(releaseAnswer)
	<-resolved
	resolution, ok := owner.Resolution(id.PromptID)
	if !ok || resolution.State != PromptCancelled {
		t.Fatalf("resolution = %+v, %v", resolution, ok)
	}
}

func TestPendingPromptOwnerCancellationDoesNotWaitForCancelCallback(t *testing.T) {
	var owner PendingPromptOwner
	id := PromptIdentity{PromptID: "p-blocked-cancel", TurnID: "t", Kind: PromptMCP}
	cancelStarted := make(chan struct{})
	releaseCancel := make(chan struct{})
	cancelDone := make(chan struct{})
	if err := owner.RegisterPrompt(PendingPrompt{Identity: id, Cancel: func() error {
		close(cancelStarted)
		<-releaseCancel
		close(cancelDone)
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	returned := make(chan struct{})
	go func() {
		owner.CancelAll()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("registry cancellation waited for a blocked cancellation callback")
	}
	<-cancelStarted
	close(releaseCancel)
	<-cancelDone
	resolution, ok := owner.Resolution(id.PromptID)
	if !ok || resolution.State != PromptCancelled {
		t.Fatalf("resolution = %+v, %v", resolution, ok)
	}
}

func TestControllerCancelSignalsTurnWhilePromptAnswererIsBlocked(t *testing.T) {
	c := newOwnedTestController(t, Options{})
	t.Cleanup(c.Close)

	turnCtx, cancelTurn := context.WithCancel(context.Background())
	c.mu.Lock()
	c.turns.cancel = cancelTurn
	c.turns.phase = session.RuntimeRunning
	c.mu.Unlock()

	id := PromptIdentity{PromptID: "p-controller-blocked", TurnID: "turn-1", Kind: PromptApproval}
	answerStarted := make(chan struct{})
	releaseAnswer := make(chan struct{})
	if err := c.promptOwner.RegisterPrompt(PendingPrompt{Identity: id, Resolve: func(PromptAnswer) error {
		close(answerStarted)
		<-releaseAnswer
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	resolved := make(chan error, 1)
	go func() { resolved <- c.promptOwner.Resolve(id, PromptAnswer{Allow: true}) }()
	<-answerStarted

	cancelReturned := make(chan struct{})
	go func() {
		c.Cancel()
		close(cancelReturned)
	}()
	select {
	case <-turnCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("Stop did not signal the active turn while its answerer was blocked")
	}
	select {
	case <-cancelReturned:
	case <-time.After(time.Second):
		t.Fatal("Stop waited for the blocked answerer")
	}

	close(releaseAnswer)
	<-resolved
	c.mu.Lock()
	c.turns.phase = session.RuntimeIdle
	c.turns.cancel = nil
	c.mu.Unlock()
}

func TestPendingPromptOwnerBindsMissingRoutingOnce(t *testing.T) {
	var owner PendingPromptOwner
	id := PromptIdentity{PromptID: "p-bind", Kind: PromptAsk}
	if err := owner.Register(id); err != nil {
		t.Fatal(err)
	}
	bound, ok := owner.BindRouting(id.PromptID, "turn-1", "runtime-1")
	if !ok || bound.TurnID != "turn-1" || bound.RuntimeEpoch != "runtime-1" {
		t.Fatalf("bound identity = %+v, %v", bound, ok)
	}
	again, ok := owner.BindRouting(id.PromptID, "turn-2", "runtime-2")
	if !ok || again != bound {
		t.Fatalf("routing identity was rewritten: first=%+v second=%+v ok=%v", bound, again, ok)
	}
}

func TestPromptAnsweredEventInheritsOwnerTurnID(t *testing.T) {
	var got event.Event
	c := newOwnedTestController(t, Options{Sink: event.FuncSink(func(e event.Event) { got = e })})
	t.Cleanup(c.Close)
	c.promptOwner.Register(PromptIdentity{PromptID: "p-event", TurnID: "turn-event", Kind: PromptAsk})
	if err := c.emitTurnEventChecked(event.Event{Kind: event.PromptAnswered, ItemID: "p-event"}); err != nil {
		t.Fatal(err)
	}
	if got.TurnID != "turn-event" {
		t.Fatalf("PromptAnswered turn id = %q", got.TurnID)
	}
}
