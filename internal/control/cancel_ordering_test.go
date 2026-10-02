package control

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/provider"
)

func TestCancelSessionTreatsIndependentBackgroundJobAsIdle(t *testing.T) {
	manager := jobs.NewManager(event.Discard)
	t.Cleanup(manager.Close)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	c := newOwnedTestController(t, Options{Jobs: manager, SessionPath: path})
	t.Cleanup(c.Close)
	started := make(chan struct{})
	manager.StartForSession(agent.BranchID(path), "bash", "background", func(ctx context.Context, _ io.Writer) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	<-started

	receipt := c.CancelSession()
	if !receipt.Accepted || !receipt.AlreadyIdle {
		t.Fatalf("idle receipt with background job = %+v", receipt)
	}
	if running := manager.RunningForSession(agent.BranchID(path)); len(running) != 1 {
		t.Fatalf("session Stop changed independent background jobs: %+v", running)
	}
}

// Stop must acknowledge after signalling the turn without waiting for the
// cancelling status to cross a synchronous event barrier.
func TestCancelSessionAcknowledgesBeforeStatusBarrier(t *testing.T) {
	releaseStatus := make(chan struct{})
	statusEntered := make(chan struct{}, 1)
	c := newOwnedTestController(t, Options{Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnStatusChanged && e.Status == event.TurnCancelling {
			statusEntered <- struct{}{}
			<-releaseStatus
		}
	})})
	t.Cleanup(c.Close)

	turnCtxDone := make(chan struct{})
	releaseTurn := make(chan struct{})
	started := make(chan struct{})
	c.runGuarded(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(turnCtxDone)
		// Hold the turn open so TurnDone cannot race ahead of the cancelling
		// status; the assertion is about ordering inside Cancel itself.
		<-releaseTurn
		return ctx.Err()
	})
	<-started
	defer close(releaseTurn)

	cancelReturned := make(chan struct{})
	go func() {
		c.CancelSession()
		close(cancelReturned)
	}()
	select {
	case <-turnCtxDone:
	case <-time.After(5 * time.Second):
		close(releaseStatus)
		t.Fatal("turn context was not cancelled before the status barrier")
	}
	select {
	case <-statusEntered:
	case <-time.After(5 * time.Second):
		close(releaseStatus)
		t.Fatal("cancel never emitted the cancelling status")
	}
	select {
	case <-cancelReturned:
	case <-time.After(5 * time.Second):
		close(releaseStatus)
		t.Fatal("CancelSession receipt waited for the status barrier")
	}
	close(releaseStatus)
}

// A cancelling status stamped for a turn that already terminated must not turn
// the next admitted turn into a permanently "cancelling" one.
func TestStaleCancellingStatusDoesNotStickToNextTurn(t *testing.T) {
	dir := t.TempDir()
	done := make(chan event.Event, 4)
	c := newOwnedTestController(t, Options{SessionDir: dir, SessionPath: dir + "/session.jsonl", Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			done <- e
		}
	})})
	t.Cleanup(c.Close)

	c.runGuarded(func(context.Context) error { return nil })
	first := waitTurnDoneEvent(t, done)
	if first.TurnID == "" {
		t.Fatal("first turn has no ledger id")
	}

	started := make(chan struct{})
	c.runGuarded(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	<-started
	c.emitTurnStatus(event.TurnCancelling, first.TurnID)
	if st := c.RuntimeStatus(); st.Status == event.TurnCancelling || st.CancelRequested {
		t.Fatalf("stale cancelling status leaked into the next turn: %+v", st)
	}
	c.Cancel()
	if second := waitTurnDoneEvent(t, done); second.Status != event.TurnInterrupted {
		t.Fatalf("second turn terminal = %q, want interrupted", second.Status)
	}
}

func TestCancellationGraceSealsUncooperativeTurnAndPreservesQueue(t *testing.T) {
	dir := t.TempDir()
	states := make(chan event.RuntimeStateSnapshot, 32)
	sink := &runtimeStateTestSink{Sink: event.Discard, states: states}
	exec := agent.New(nil, nil, agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, SessionDir: dir, SessionPath: dir + "/session.jsonl", Sink: sink})
	c.testCancelGrace = 20 * time.Millisecond
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		for deadline := time.Now().Add(5 * time.Second); c.Running() && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		c.Close()
	})

	started := make(chan struct{})
	if got := c.runGuarded(func(context.Context) error {
		close(started)
		<-release // deliberately ignores cancellation
		return nil
	}); got != turnStarted {
		t.Fatalf("admission = %v", got)
	}
	<-started
	c.Cancel()
	queuedStarted := make(chan struct{})
	if got := c.runGuarded(func(context.Context) error { close(queuedStarted); return nil }); got != turnParked {
		t.Fatalf("cancelling admission = %v, want queued", got)
	}

	recovery := runtimeStateAwait(t, states, func(state event.RuntimeStateSnapshot) bool {
		return state.Phase == "recovery_required"
	})
	if recovery.Recovery == nil || recovery.Recovery.State != "recovery_required" || recovery.Cancellable {
		t.Fatalf("recovery snapshot = %+v", recovery)
	}
	if !c.Running() {
		t.Fatal("uncooperative worker ownership was released before exit")
	}

	if got := c.runGuarded(func(context.Context) error { return nil }); got != turnDroppedWriteAuthority {
		t.Fatalf("recovery admission = %v, want rejected", got)
	}
	select {
	case <-queuedStarted:
		t.Fatal("turn started while recovery was required")
	default:
	}

	// A late semantic result from the sealed worker cannot mutate the durable
	// todo projection.
	c.sink.Emit(event.Event{Kind: event.ToolResult, Tool: event.Tool{
		Name: "todo_write", TodoWritten: true,
		Todos: []event.Todo{{Content: "late", Status: "completed"}},
	}})
	if todos, written := c.turnEventLedger().TodoState(); written || len(todos) != 0 {
		t.Fatalf("late todo committed after recovery: written=%v todos=%+v", written, todos)
	}
	v3 := c.sessionEventStore().Snapshot().Projection
	if v3.TodoWritten || len(v3.Todos) != 0 {
		t.Fatalf("late todo committed to v3 after recovery: written=%v todos=%+v", v3.TodoWritten, v3.Todos)
	}
	if v3.Recovery == nil || v3.Recovery.State != "recovery_required" || v3.Recovery.Reason != "cancellation_grace_expired" {
		t.Fatalf("v3 recovery seal = %+v", v3.Recovery)
	}

	releaseOnce.Do(func() { close(release) })
	deadline := time.Now().Add(5 * time.Second)
	for c.Running() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.Running() || c.RuntimeStateSnapshot().Phase != "recovery_required" {
		t.Fatalf("worker exit changed recovery boundary: %+v", c.RuntimeStateSnapshot())
	}
}

func TestRecoverySealDropsLateTranscriptOutput(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/session.jsonl"
	session := agent.NewSession("system")
	session.Add(provider.Message{Role: provider.RoleUser, ID: "user", Content: "do work"})
	session.Add(provider.Message{Role: provider.RoleAssistant, ID: "late-message", Content: "must stay diagnostic-only"})
	exec := agent.New(nil, nil, session, agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, SessionDir: dir, SessionPath: path, Sink: event.Discard})
	t.Cleanup(c.Close)

	ledger := c.turnEventLedger()
	if _, err := ledger.Begin(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ledger.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("append turn start: ok=%v err=%v", ok, err)
	}
	if _, ok, err := ledger.Append(event.Event{Kind: event.TurnDone, Recovery: &event.RecoveryStatus{State: "recovery_required"}}, event.TurnRecoveryRequired); err != nil || !ok {
		t.Fatalf("append recovery seal: ok=%v err=%v", ok, err)
	}
	c.finishInFlightTurn(1, agent.InFlightTurnMeta{ID: "turn", StartMessageIndex: 1, PreserveUser: true})
	history := c.History()
	if len(history) != 2 || history[1].ID != "user" {
		t.Fatalf("recovery-sealed history = %+v", history)
	}
	for _, message := range history {
		if message.ID == "late-message" {
			t.Fatal("late sealed-worker message entered the durable transcript")
		}
	}
}
