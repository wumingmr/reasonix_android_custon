package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/event"
)

type exactTurnRunner struct {
	once    sync.Once
	started chan struct{}
}

func (r *exactTurnRunner) Run(ctx context.Context, _ string) error {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	return ctx.Err()
}

func cleanupExactTurnController(t *testing.T, ctrl *control.Controller) {
	t.Helper()
	t.Cleanup(func() {
		ctrl.Close()
		select {
		case <-ctrl.Closed():
		case <-time.After(5 * time.Second):
			t.Error("Controller teardown did not finish before removing its storage")
		}
	})
}

func TestTurnRuntimeAPIRoutesStopAnswerAndReplayByExactTurn(t *testing.T) {
	dir := t.TempDir()
	runner := &exactTurnRunner{started: make(chan struct{})}
	sink := &tabEventSink{tabID: "tab", ctx: context.Background()}
	terminal := make(chan event.Event, 1)
	sink.SetBotSink(event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			terminal <- e
		}
	}))
	ctrl := control.New(control.Options{
		Runner: runner, Sink: sink, SessionDir: dir,
		SessionPath: filepath.Join(dir, "session.jsonl"),
	})
	cleanupExactTurnController(t, ctrl)
	tab := &WorkspaceTab{ID: "tab", Scope: "global", Ready: true, Ctrl: ctrl, sink: sink}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}, activeTabID: tab.ID}
	sink.app = app

	start, err := app.StartTurnForTab(tab.ID, "hold this turn", "submission-1")
	if err != nil {
		t.Fatalf("StartTurnForTab: %v", err)
	}
	if !strings.HasPrefix(start.TurnID, "turn_") || start.SubmissionID != "submission-1" {
		t.Fatalf("start receipt = %+v, want stable turn and submission ids", start)
	}
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn runner did not start")
	}

	if err := app.AnswerPromptForTab(tab.ID, "turn_stale", "prompt-1", nil); err == nil {
		t.Fatal("stale turn id answered an active turn prompt")
	}
	if _, err := app.EnqueueInboxSteerForTurn(tab.ID, "turn_stale", "late steer", "late steer", ""); err == nil {
		t.Fatal("stale turn id steered the active turn")
	}
	if err := app.AnswerPromptForTab(tab.ID, start.TurnID, "already-answered", nil); err != nil {
		t.Fatalf("same-turn duplicate/unknown answer should be idempotent: %v", err)
	}

	before, err := app.TurnEventsForTab(tab.ID, 0)
	if err != nil {
		t.Fatalf("TurnEventsForTab before cancel: %v", err)
	}
	if len(before.Events) == 0 || before.Events[0].Status != event.TurnQueued {
		t.Fatalf("events before cancel = %+v, want durable queued prefix", before)
	}
	if err := app.InterruptTurnForTab(tab.ID, start.TurnID); err != nil {
		t.Fatalf("InterruptTurnForTab: %v", err)
	}
	select {
	case done := <-terminal:
		if done.Status != event.TurnInterrupted {
			t.Fatalf("terminal status = %q, want interrupted", done.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not reach terminal state after exact interrupt")
	}
	after, err := app.TurnEventsForTab(tab.ID, before.Events[len(before.Events)-1].Sequence)
	if err != nil {
		t.Fatalf("TurnEventsForTab after cancel: %v", err)
	}
	if len(after.Events) == 0 || after.Events[len(after.Events)-1].Status != event.TurnInterrupted {
		t.Fatalf("events after cancel = %+v, want non-nil interrupted suffix", after)
	}
	empty, err := app.TurnEventsForTab(tab.ID, after.Events[len(after.Events)-1].Sequence)
	if err != nil {
		t.Fatalf("empty replay: %v", err)
	}
	if empty.Events == nil || len(empty.Events) != 0 {
		t.Fatalf("empty replay events = %#v, want []", empty.Events)
	}
}

// A Stop button rendered for an earlier turn must still stop the turn that is
// running now; only an idle tab is reported back, with a stable code.
func TestInterruptTurnForTabStopsActiveWorkDespiteStaleTurnID(t *testing.T) {
	dir := t.TempDir()
	runner := &exactTurnRunner{started: make(chan struct{})}
	sink := &tabEventSink{tabID: "tab", ctx: context.Background()}
	terminal := make(chan event.Event, 1)
	sink.SetBotSink(event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			terminal <- e
		}
	}))
	ctrl := control.New(control.Options{
		Runner: runner, Sink: sink, SessionDir: dir,
		SessionPath: filepath.Join(dir, "session.jsonl"),
	})
	cleanupExactTurnController(t, ctrl)
	tab := &WorkspaceTab{ID: "tab", Scope: "global", Ready: true, Ctrl: ctrl, sink: sink}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}, activeTabID: tab.ID}
	sink.app = app

	if err := app.InterruptTurnForTab(tab.ID, "turn_none"); err != nil {
		t.Fatalf("idle stop = %v, want idempotent success", err)
	}
	if receipt, err := app.CancelSessionForTab(tab.ID); err != nil || !receipt.Accepted || !receipt.AlreadyIdle {
		t.Fatalf("idle cancel receipt = %+v, %v", receipt, err)
	}

	if _, err := app.StartTurnForTab(tab.ID, "hold this turn", "submission-1"); err != nil {
		t.Fatalf("StartTurnForTab: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn runner did not start")
	}
	if err := app.InterruptTurnForTab(tab.ID, "turn_stale"); err != nil {
		t.Fatalf("stale-id stop = %v, want the active turn interrupted", err)
	}
	select {
	case done := <-terminal:
		if done.Status != event.TurnInterrupted {
			t.Fatalf("terminal status = %q, want interrupted", done.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not reach terminal state after stale-id stop")
	}
}

func TestStartTurnForTabReturnsManagementDispositionWithoutTurnID(t *testing.T) {
	dir := t.TempDir()
	sink := &tabEventSink{tabID: "tab", ctx: context.Background()}
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: filepath.Join(dir, "session.jsonl"), Sink: sink})
	cleanupExactTurnController(t, ctrl)
	tab := &WorkspaceTab{ID: "tab", Scope: "global", Ready: true, Ctrl: ctrl, sink: sink}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}, activeTabID: tab.ID}
	sink.app = app

	start, err := app.StartTurnForTab(tab.ID, "/context", "submission-management")
	if err != nil {
		t.Fatalf("StartTurnForTab management command: %v", err)
	}
	if start.Disposition != control.SubmitManagementHandled || start.TurnID != "" {
		t.Fatalf("management receipt = %+v, want management_handled without turn id", start)
	}
	replay, err := app.TurnEventsForTab(tab.ID, 0)
	if err != nil {
		t.Fatalf("TurnEventsForTab: %v", err)
	}
	if len(replay.Events) != 0 {
		t.Fatalf("management command created durable turn events: %+v", replay.Events)
	}
}

func TestStartTurnForTabRejectsManagementDuringActiveTurn(t *testing.T) {
	dir := t.TempDir()
	runner := &exactTurnRunner{started: make(chan struct{})}
	sink := &tabEventSink{tabID: "tab", ctx: context.Background()}
	terminal := make(chan event.Event, 1)
	sink.SetBotSink(event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			terminal <- e
		}
	}))
	ctrl := control.New(control.Options{
		Runner: runner, Sink: sink, SessionDir: dir,
		SessionPath: filepath.Join(dir, "session.jsonl"),
	})
	cleanupExactTurnController(t, ctrl)
	tab := &WorkspaceTab{ID: "tab", Scope: "global", Ready: true, Ctrl: ctrl, sink: sink}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}, activeTabID: tab.ID}
	sink.app = app

	start, err := app.StartTurnForTab(tab.ID, "hold this turn", "submission-active")
	if err != nil {
		t.Fatalf("StartTurnForTab active turn: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn runner did not start")
	}
	if _, err := app.StartTurnForTab(tab.ID, "/context", "submission-management"); !errors.Is(err, control.ErrTurnRunning) {
		t.Fatalf("management command during active turn = %v, want ErrTurnRunning", err)
	}
	status := ctrl.RuntimeStatus()
	if !status.Running || status.TurnID != start.TurnID {
		t.Fatalf("active turn changed after rejected management command: %+v", status)
	}
	ctrl.Cancel()
	select {
	case <-terminal:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not finish after cancellation")
	}
}

// The turn id belongs to the controller that admitted the submission. A tab
// whose controller is replaced before the reply is built must still answer
// with that receipt instead of asking the replacement.
func TestStartTurnForTabAnswersWithAdmittingControllerReceipt(t *testing.T) {
	dir := t.TempDir()
	runner := &exactTurnRunner{started: make(chan struct{})}
	sink := &tabEventSink{tabID: "tab", ctx: context.Background()}
	tab := &WorkspaceTab{ID: "tab", Scope: "global", Ready: true, sink: sink}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}, activeTabID: tab.ID}
	sink.app = app

	replacementDir := t.TempDir()
	replacement := control.New(control.Options{SessionDir: replacementDir, SessionPath: filepath.Join(replacementDir, "session.jsonl")})
	cleanupExactTurnController(t, replacement)
	var swap sync.Once
	ctrl := control.New(control.Options{
		Runner: runner, SessionDir: dir, SessionPath: filepath.Join(dir, "session.jsonl"),
		Sink: event.FuncSink(func(e event.Event) {
			sink.Emit(e)
			if e.Kind == event.TurnStarted {
				swap.Do(func() {
					app.mu.Lock()
					tab.Ctrl = replacement
					app.mu.Unlock()
				})
			}
		}),
	})
	cleanupExactTurnController(t, ctrl)
	tab.Ctrl = ctrl

	start, err := app.StartTurnForTab(tab.ID, "hold this turn", "submission-swapped")
	if err != nil {
		t.Fatalf("StartTurnForTab: %v", err)
	}
	if want := ctrl.TurnIDForSubmission("submission-swapped"); want == "" || start.TurnID != want {
		t.Fatalf("turn id = %q, want the admitting controller's receipt %q", start.TurnID, want)
	}
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn runner did not start")
	}
	ctrl.Cancel()
}
