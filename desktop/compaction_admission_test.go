package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

type compactionAdmissionSink struct {
	*tabEventSink
	states chan event.RuntimeStateSnapshot
}

func (s *compactionAdmissionSink) RuntimeStateChanged(state event.RuntimeStateSnapshot) {
	s.states <- state
}

func awaitCompactionIdle(t *testing.T, ctrl *control.Controller, states <-chan event.RuntimeStateSnapshot) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case state := <-states:
			if !state.Running && state.Maintenance == nil && !ctrl.RuntimeStatus().Running {
				return
			}
		case <-timeout:
			t.Fatal("maintenance did not release its runtime owner")
		}
	}
}

func TestCompactionAdmissionReleasesManagementReservation(t *testing.T) {
	for _, name := range []string{"receipt", "legacy", "display"} {
		t.Run(name, func(t *testing.T) {
			prov := &compactReceiptProvider{started: make(chan struct{}, 8), release: make(chan struct{}, 8)}
			sess := agent.NewSession("system")
			for range 8 {
				sess.Add(provider.Message{Role: provider.RoleUser, Content: strings.Repeat("question ", 300)})
				sess.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("answer ", 300)})
			}
			sink := &compactionAdmissionSink{tabEventSink: &tabEventSink{tabID: "compact"}, states: make(chan event.RuntimeStateSnapshot, 128)}
			exec := agent.New(prov, tool.NewRegistry(), sess, agent.Options{ContextWindow: 32_000}, sink)
			dir := t.TempDir()
			service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(dir, "sessions-v5")))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
			runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "compact"})
			if err != nil {
				t.Fatal(err)
			}
			ctrl := control.New(control.Options{Runner: exec, Executor: exec, Sink: sink, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
			cleanupExactTurnController(t, ctrl)
			tab := &WorkspaceTab{ID: "compact", Scope: "global", Ready: true, Ctrl: ctrl, sink: sink.tabEventSink}
			app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}, activeTabID: tab.ID}
			submit := func(input, id string) error {
				switch name {
				case "legacy":
					return app.SubmitToTabWithID(tab.ID, input, id)
				case "display":
					return app.SubmitDisplayToTabWithID(tab.ID, input, input, id)
				}
				_, err := app.StartTurnForTab(tab.ID, input, id)
				return err
			}

			for cycle := range 2 {
				if err := submit("/compact preserve decisions", "compact"); err != nil {
					t.Fatal(err)
				}
				select {
				case <-prov.started:
				case <-time.After(10 * time.Second):
					t.Fatal("summary provider did not start")
				}
				if _, err := app.StartTurnForTab(tab.ID, "must remain blocked", "blocked"); !errors.Is(err, control.ErrTurnRunning) {
					t.Fatalf("active maintenance lost admission fence: %v", err)
				}
				prov.release <- struct{}{}
				awaitCompactionIdle(t, ctrl, sink.states)
				if !sink.tryBeginTurn("probe") {
					t.Fatal("completed maintenance retained desktop turn reservation")
				}
				sink.cancelTurnStart()
				// A new send must reach the provider and supply history for compaction.
				if err := submit(strings.Repeat("new task detail ", 500), fmt.Sprintf("next-%d", cycle)); err != nil {
					t.Fatalf("next send: %v", err)
				}
				select {
				case <-prov.started:
				case <-time.After(10 * time.Second):
					t.Fatal("next send did not reach provider")
				}
				prov.release <- struct{}{}
				awaitCompactionIdle(t, ctrl, sink.states)
			}
		})
	}
}

func TestManagementAdmissionDoesNotReleaseStartedTurn(t *testing.T) {
	sink := &tabEventSink{}
	if !sink.tryBeginTurn("management") {
		t.Fatal("reserve management")
	}
	sink.Emit(event.Event{Kind: event.TurnStarted})
	sink.cancelTurnStart()
	if sink.tryBeginTurn("new") {
		t.Fatal("management completion released an independently started turn")
	}
	sink.Emit(event.Event{Kind: event.TurnDone})
	if !sink.tryBeginTurn("new") {
		t.Fatal("completed turn retained reservation")
	}
}

func TestManagementAdmissionPreservesTurnDoneFanout(t *testing.T) {
	sink := &tabEventSink{}
	gate := &turnFanoutGate{kind: event.TurnDone, entered: make(chan struct{}), release: make(chan struct{})}
	sink.SetBotSink(gate)
	if !sink.tryBeginTurn("management") {
		t.Fatal("reserve management")
	}
	sink.Emit(event.Event{Kind: event.TurnStarted})
	done := make(chan struct{})
	go func() { sink.Emit(event.Event{Kind: event.TurnDone}); close(done) }()
	select {
	case <-gate.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("TurnDone fan-out did not start")
	}
	sink.cancelTurnStart()
	admitted := sink.tryBeginTurn("new")
	close(gate.release)
	<-done
	if admitted {
		t.Fatal("management completion bypassed TurnDone fan-out fence")
	}
	if !sink.tryBeginTurn("new") {
		t.Fatal("settled fan-out retained reservation")
	}
}
