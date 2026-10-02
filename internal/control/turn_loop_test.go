package control

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func exclusiveTestController(t *testing.T, sink event.Sink) (*Controller, *session.Service, *session.Runtime) {
	t.Helper()
	if sink == nil {
		sink = event.Discard
	}
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "loop"})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(testutil.NewMock("test"), tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, sink)
	c := newOwnedTestController(t, Options{
		Runner: exec, Executor: exec, Sink: sink,
		SessionService: service, SessionRuntime: runtime, ExclusiveSession: true,
	})
	return c, service, runtime
}

func TestIdleCancelThenSendProducesNewTurnID(t *testing.T) {
	done := make(chan event.Event, 4)
	c, _, runtime := exclusiveTestController(t, event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			done <- e
		}
	}))
	receipt := c.CancelSession()
	if !receipt.Accepted || !receipt.AlreadyIdle {
		t.Fatalf("idle cancel = %+v", receipt)
	}
	started := make(chan struct{})
	if got := c.runGuarded(func(context.Context) error {
		close(started)
		return nil
	}); got != turnStarted {
		t.Fatalf("send after idle cancel = %v", got)
	}
	<-started
	terminal := waitTurnDoneEvent(t, done)
	if terminal.TurnID == "" {
		t.Fatal("first turn after idle cancel has no durable turn id")
	}
	waitIdleAdmission(t, c)
	if runtime.StateSnapshot().Phase != session.RuntimeIdle {
		t.Fatalf("runtime phase = %s", runtime.StateSnapshot().Phase)
	}
}

func TestCancelDuringStreamingAndToolAndApproval(t *testing.T) {
	for _, name := range []string{"stream", "tool", "approval"} {
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			c := newOwnedTestController(t, Options{Sink: event.Discard})
			t.Cleanup(c.Close)
			c.runGuarded(func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			})
			<-started
			c.CancelSession()
			waitIdleAdmission(t, c)
		})
	}
}

func TestInputAfterCancelWakesOnceFIFO(t *testing.T) {
	var ran atomic.Int32
	order := make(chan int, 2)
	started := make(chan struct{})
	release := make(chan struct{})
	c := newOwnedTestController(t, Options{Sink: event.Discard})
	t.Cleanup(c.Close)
	c.runGuarded(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		<-release
		return ctx.Err()
	})
	<-started
	c.CancelSession()
	if got := c.runGuarded(func(context.Context) error {
		ran.Add(1)
		order <- 1
		return nil
	}); got != turnParked {
		t.Fatalf("first queued = %v, want parked", got)
	}
	if got := c.runGuarded(func(context.Context) error {
		ran.Add(1)
		order <- 2
		return nil
	}); got != turnParked {
		t.Fatalf("second queued = %v, want parked", got)
	}
	close(release)
	waitIdleAdmission(t, c)
	if ran.Load() != 2 {
		t.Fatalf("queued bodies ran %d times, want 2", ran.Load())
	}
	if first, second := <-order, <-order; first != 1 || second != 2 {
		t.Fatalf("fifo order = %d,%d", first, second)
	}
}

func TestSlowExitDoesNotStartNextTurn(t *testing.T) {
	started := make(chan struct{})
	exit := make(chan struct{})
	nextStarted := make(chan struct{})
	c := newOwnedTestController(t, Options{Sink: event.Discard})
	t.Cleanup(c.Close)
	c.runGuarded(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		<-exit
		return ctx.Err()
	})
	<-started
	c.CancelSession()
	if got := c.runGuarded(func(context.Context) error {
		close(nextStarted)
		return nil
	}); got != turnParked {
		t.Fatalf("next admission = %v, want parked until slow exit", got)
	}
	select {
	case <-nextStarted:
		t.Fatal("next turn started before the cancelled body exited")
	case <-time.After(50 * time.Millisecond):
	}
	close(exit)
	select {
	case <-nextStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("queued turn did not start after slow exit")
	}
	waitIdleAdmission(t, c)
}

func TestDuplicateStopAndConcurrentFinish(t *testing.T) {
	started := make(chan struct{})
	c := newOwnedTestController(t, Options{Sink: event.Discard})
	t.Cleanup(c.Close)
	c.runGuarded(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	<-started
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			c.CancelSession()
			c.Cancel()
		})
	}
	wg.Wait()
	waitIdleAdmission(t, c)
}

func TestEachStartedTurnHasOneTerminalEvent(t *testing.T) {
	var terminals atomic.Int32
	c := newOwnedTestController(t, Options{Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			terminals.Add(1)
		}
	})})
	t.Cleanup(c.Close)
	for range 3 {
		c.runGuarded(func(context.Context) error { return nil })
		waitIdleAdmission(t, c)
	}
	if got := terminals.Load(); got != 3 {
		t.Fatalf("terminal events = %d, want 3", got)
	}
}

func TestStopHistoryReplaceThenSendGetsNewTurnID(t *testing.T) {
	done := make(chan event.Event, 8)
	c, service, runtime := exclusiveTestController(t, event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			done <- e
		}
	}))
	if err := c.RecordSessionMessages(t.Context(), "before-stop", []provider.Message{
		{ID: "retained-question", Role: provider.RoleUser, Content: "keep this question"},
	}); err != nil {
		t.Fatal(err)
	}
	c.restoreExecutorFromSessionEvents()
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Query().HistoryShape(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	c.runGuarded(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		c.replaceSessionAfterCancel(c.executor.Session().Snapshot())
		return ctx.Err()
	})
	<-started
	c.CancelSession()
	first := waitTurnDoneEvent(t, done)
	if first.TurnID == "" {
		t.Fatal("cancelled turn has no durable id")
	}
	waitIdleAdmission(t, c)
	if err := c.turnEventLedgerError(); err != nil {
		t.Fatalf("cancel poisoned admission: %v", err)
	}
	// Switching after Stop reads the same durable rewrite through the history
	// index. Admission alone used to pass while these reads failed with a
	// duplicate (message_id, version) key.
	other, err := service.Create(t.Context(), session.CreateOptions{SessionID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenSession(t.Context(), other.Ref()); err != nil {
		t.Fatalf("switch away after stop: %v", err)
	}
	if _, err := service.Query().HistoryShape(t.Context(), runtime.Ref()); err != nil {
		t.Fatalf("read stopped session after switching away: %v", err)
	}
	if _, err := c.OpenSession(t.Context(), runtime.Ref()); err != nil {
		t.Fatalf("switch back after stop: %v", err)
	}
	page, err := service.Query().ReadHistoryWindow(t.Context(), runtime.Ref(), session.HistoryWindowRequest{Anchor: "newest"})
	if err != nil || page.Status != "ready" {
		t.Fatalf("reopened stopped history: %+v, %v", page, err)
	}
	found := false
	for _, message := range page.Messages {
		found = found || message.MessageID == "retained-question"
	}
	if !found {
		t.Fatal("stopped history lost the retained question")
	}
	secondStarted := make(chan struct{})
	if got := c.runGuarded(func(context.Context) error {
		close(secondStarted)
		return nil
	}); got != turnStarted {
		t.Fatalf("resend after cancel = %v", got)
	}
	<-secondStarted
	second := waitTurnDoneEvent(t, done)
	if second.TurnID == "" || second.TurnID == first.TurnID {
		t.Fatalf("second turn id = %q, first = %q", second.TurnID, first.TurnID)
	}
	waitIdleAdmission(t, c)
	if runtime.StateSnapshot().Phase != session.RuntimeIdle {
		t.Fatalf("runtime phase = %s", runtime.StateSnapshot().Phase)
	}
}

func TestPlanStateTailWriteDuringStopDoesNotPoison(t *testing.T) {
	c, _, runtime := exclusiveTestController(t, event.Discard)
	started := make(chan struct{})
	c.runGuarded(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		if err := c.appendDomainState("plan/state", []byte(`{"enabled":true}`), "stop-race"); err != nil {
			t.Errorf("plan/state during stop: %v", err)
		}
		return ctx.Err()
	})
	<-started
	c.CancelSession()
	waitIdleAdmission(t, c)
	if err := c.turnEventLedgerError(); err != nil {
		t.Fatalf("plan/state vs stop poisoned ledger: %v", err)
	}
	if got := c.runGuarded(func(context.Context) error { return nil }); got != turnStarted {
		t.Fatalf("admission after plan/state race = %v", got)
	}
	waitIdleAdmission(t, c)
	if runtime.StateSnapshot().Phase != session.RuntimeIdle {
		t.Fatalf("runtime phase = %s", runtime.StateSnapshot().Phase)
	}
}

func TestTimeoutRecoveryDropsLateEventsFromNewTurn(t *testing.T) {
	c := newOwnedTestController(t, Options{Sink: event.Discard, SessionDir: t.TempDir(), SessionPath: filepath.Join(t.TempDir(), "session.jsonl")})
	t.Cleanup(c.Close)
	c.testCancelGrace = 10 * time.Millisecond
	started := make(chan struct{})
	hold := make(chan struct{})
	exited := make(chan struct{})
	c.runGuarded(func(ctx context.Context) error {
		defer close(exited)
		close(started)
		<-ctx.Done()
		<-hold
		return ctx.Err()
	})
	<-started
	c.CancelSession()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		phase := c.turns.phase
		c.mu.Unlock()
		if phase == session.RuntimeRecoveryRequired {
			break
		}
		time.Sleep(time.Millisecond)
	}
	late := event.Event{Kind: event.ToolResult, TurnID: "not-the-next-turn", Tool: event.Tool{ID: "late", Name: "bash"}}
	if !c.discardLateTurnEvent(late) {
		t.Fatal("late tool result must not apply to a later turn")
	}
	if got := c.runGuarded(func(context.Context) error { return nil }); got != turnDroppedWriteAuthority {
		t.Fatalf("admission during recovery = %v, want blocked", got)
	}
	close(hold)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("timed-out turn did not exit after release")
	}
	c.autosaveWG.Wait()
}

func TestOldControllerUnbindDoesNotClearNewGeneration(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "handoff"})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(testutil.NewMock("test"), tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	first := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	first.mu.Lock()
	oldGen := first.turns.generation
	first.mu.Unlock()
	second := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	t.Cleanup(func() { first.Close(); second.Close() })
	if err := second.ActivateSessionExecution(oldGen); err != nil {
		t.Fatalf("activate replacement: %v", err)
	}
	runtime.UnbindExecution(oldGen)
	started := make(chan struct{})
	second.runGuarded(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	<-started
	if !runtime.Cancel() {
		t.Fatal("new generation lost Stop after old unbind")
	}
	waitIdleAdmission(t, second)
}

func TestOneRuntimeCannotRunTwoControllerLoops(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "single-loop"})
	if err != nil {
		t.Fatal(err)
	}
	newController := func() *Controller {
		exec := agent.New(testutil.NewMock("test"), tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
		return newOwnedTestController(t, Options{
			Runner: exec, Executor: exec, Sink: event.Discard,
			SessionService: service, SessionRuntime: runtime, ExclusiveSession: true,
		})
	}
	first := newController()
	second := newController()
	t.Cleanup(func() { first.Close(); second.Close() })

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	if got := first.runGuarded(func(context.Context) error {
		close(firstStarted)
		<-releaseFirst
		return nil
	}); got != turnStarted {
		t.Fatalf("first admission = %v, want started", got)
	}
	<-firstStarted

	secondStarted := make(chan struct{})
	if got := second.runGuarded(func(context.Context) error {
		close(secondStarted)
		return nil
	}); got == turnStarted {
		t.Fatal("same session runtime admitted a second controller loop concurrently")
	}
	select {
	case <-secondStarted:
		t.Fatal("second controller body ran")
	default:
	}
	close(releaseFirst)
	waitIdleAdmission(t, first)
}

func TestReplacementModelContextCommitsOnlyWithExecutionCutover(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "atomic-cutover"})
	if err != nil {
		t.Fatal(err)
	}
	newController := func() *Controller {
		exec := agent.New(testutil.NewMock("test"), tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
		return newOwnedTestController(t, Options{
			Runner: exec, Executor: exec, Sink: event.Discard,
			SessionService: service, SessionRuntime: runtime, ExclusiveSession: true,
		})
	}
	first := newController()
	second := newController()
	t.Cleanup(func() { first.Close(); second.Close() })
	oldGeneration := first.ExecutionGeneration()
	before := runtime.Session().ExecutionSnapshot().EventSequence
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "replacement system"},
		{Role: provider.RoleUser, Content: "preserve me"},
	}
	if err := second.AdoptRebuiltModelContext(messages); err != nil {
		t.Fatalf("stage replacement context: %v", err)
	}
	if got := runtime.Session().ExecutionSnapshot().EventSequence; got != before {
		t.Fatalf("unpublished candidate changed sequence from %d to %d", before, got)
	}
	if !runtime.OwnsExecution(oldGeneration) {
		t.Fatal("staging replacement context stole outgoing execution ownership")
	}
	if err := ActivateControllerReplacement(first, second); err != nil {
		t.Fatalf("activate replacement: %v", err)
	}
	after := runtime.Session().ExecutionSnapshot()
	if after.EventSequence <= before {
		t.Fatalf("activation did not commit staged model context: before=%d after=%d", before, after.EventSequence)
	}
	if !runtime.OwnsExecution(second.ExecutionGeneration()) || runtime.OwnsExecution(oldGeneration) {
		t.Fatal("execution ownership did not transfer atomically")
	}
	if got := after.Projection.ModelMessages; len(got) != len(messages) || got[len(got)-1].Content != "preserve me" {
		t.Fatalf("committed model context = %+v", got)
	}
}

func TestDiscardedReplacementLeavesOutgoingExecutionOwner(t *testing.T) {
	first, service, runtime := exclusiveTestController(t, event.Discard)
	exec := agent.New(testutil.NewMock("test"), tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	candidate := newOwnedTestController(t, Options{
		Runner: exec, Executor: exec, Sink: event.Discard,
		SessionService: service, SessionRuntime: runtime, ExclusiveSession: true,
	})
	oldGeneration := first.ExecutionGeneration()
	if err := candidate.AdoptRebuiltModelContext([]provider.Message{{Role: provider.RoleSystem, Content: "discarded"}}); err != nil {
		t.Fatal(err)
	}
	candidate.ReleaseResources()
	if !runtime.OwnsExecution(oldGeneration) {
		t.Fatal("discarded candidate cleared outgoing execution owner")
	}
	started := make(chan struct{})
	if got := first.runGuarded(func(context.Context) error { close(started); return nil }); got != turnStarted {
		t.Fatalf("outgoing admission after candidate discard = %v", got)
	}
	<-started
	waitIdleAdmission(t, first)
}

func TestCloseDropsLatchedWake(t *testing.T) {
	done := make(chan event.Event, 2)
	started := make(chan struct{})
	exit := make(chan struct{})
	nextStarted := make(chan struct{})
	c := newOwnedTestController(t, Options{Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			done <- e
		}
	})})
	c.runGuarded(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		<-exit
		return ctx.Err()
	})
	<-started
	c.CancelSession()
	if got := c.runGuarded(func(context.Context) error {
		close(nextStarted)
		return nil
	}); got != turnParked {
		t.Fatalf("wake during abort = %v, want parked", got)
	}
	c.Close()
	close(exit)
	waitTurnDoneEvent(t, done)
	select {
	case <-nextStarted:
		t.Fatal("close started a latched wake")
	default:
	}
}

func TestCloseKeepsExecutionBoundUntilTerminalPublicationCompletes(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	c, service, runtime := exclusiveTestController(t, holdFinishingWindow(release, entered, nil))
	generation := c.ExecutionGeneration()
	started := make(chan struct{})
	if got := c.runGuarded(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}); got != turnStarted {
		t.Fatalf("admission = %v, want started", got)
	}
	<-started
	c.Close()
	<-entered

	if got := runtime.StateSnapshot().Phase; got != session.RuntimeFinalizing {
		t.Fatalf("runtime phase while terminal publication is blocked = %s, want finalizing", got)
	}
	if !runtime.OwnsExecution(generation) {
		t.Fatal("close released execution ownership before terminal publication")
	}
	if current, ok := service.Runtime(runtime.Ref()); !ok || current != runtime {
		t.Fatal("close retired the session runtime before terminal publication")
	}

	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !runtime.OwnsExecution(generation) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("execution ownership was not released after terminal publication")
}

func TestSessionOpenFailureStillFailsClosed(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan event.Event, 1)
	c := newOwnedTestController(t, Options{
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.TurnDone {
				done <- e
			}
		}),
		SessionDir: t.TempDir(), SessionPath: filepath.Join(blocked, "session.jsonl"),
	})
	t.Cleanup(c.Close)
	c.Submit("must fail closed")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("open failure did not complete the admission attempt")
	}
	if err := c.turnEventLedgerError(); err == nil {
		t.Fatal("open failure did not fail closed")
	}
}
