package cli

import (
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/session"
)

type shutdownSnapshotSpy struct {
	control.SessionAPI
	err           error
	started       chan<- struct{}
	release       <-chan struct{}
	snapshotCalls atomic.Int32
	shutdownCalls atomic.Int32
}

// The spy stands for a controller whose session identity is gone: the reclaim
// path reads both locators and must find neither.
func (s *shutdownSnapshotSpy) SessionRef() (session.SessionRef, bool) {
	return session.SessionRef{}, false
}

func (s *shutdownSnapshotSpy) SessionPath() string { return "" }

func (s *shutdownSnapshotSpy) Snapshot() error {
	s.snapshotCalls.Add(1)
	return nil
}

func (s *shutdownSnapshotSpy) SnapshotForShutdown() error {
	s.shutdownCalls.Add(1)
	if s.started != nil {
		s.started <- struct{}{}
	}
	if s.release != nil {
		<-s.release
	}
	return s.err
}

func TestTUIShutdownUsesRecoveringSnapshotAndKeepsFailure(t *testing.T) {
	wantErr := errors.New("final snapshot failed")
	ctrl := &shutdownSnapshotSpy{err: wantErr}
	m := newTestChatTUI()
	m.ctrl = ctrl
	completion := newTUIShutdownCompletion()

	next, cmd := m.update(tuiShutdownMsg{completion: completion})
	got := next.(chatTUI)
	if cmd == nil || cmd() != (tea.QuitMsg{}) {
		t.Fatal("shutdown message did not return tea.Quit")
	}
	if calls := ctrl.snapshotCalls.Load(); calls != 0 {
		t.Fatalf("plain Snapshot calls = %d, want 0", calls)
	}
	if calls := ctrl.shutdownCalls.Load(); calls != 1 {
		t.Fatalf("SnapshotForShutdown calls = %d, want 1", calls)
	}
	if !errors.Is(got.shutdownErr, wantErr) {
		t.Fatalf("shutdownErr = %v, want %v", got.shutdownErr, wantErr)
	}
	select {
	case <-completion.done:
	default:
		t.Fatal("shutdown completion was not acknowledged after the final snapshot")
	}
}

// TestTUIShutdownSignalQuitsAfterReclaim pins the post-reclaim contract: once
// the remote side owns the session, SIGHUP/SIGTERM terminate the process like
// any other exit, without snapshotting a session this TUI no longer writes.
// Consuming the signal here left an orphan after an SSH drop and forced
// SIGKILL under systemctl stop.
func TestTUIShutdownSignalQuitsAfterReclaim(t *testing.T) {
	cases := []struct {
		name  string
		setup func(m *chatTUI)
	}{
		{"after the reclaim callback", func(m *chatTUI) { m.sessionReclaimed = true }},
		{"after the return but before the callback", func(m *chatTUI) {
			m.takeover = newCLITakeoverManager(nil, nil)
			m.takeover.returned.Store(true)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := &shutdownSnapshotSpy{}
			m := newTestChatTUI()
			m.ctrl = ctrl
			tc.setup(&m)
			completion := newTUIShutdownCompletion()

			_, cmd := m.update(tuiShutdownMsg{completion: completion})
			if cmd == nil || cmd() != (tea.QuitMsg{}) {
				t.Fatal("signal shutdown after reclaim did not return tea.Quit")
			}
			if calls := ctrl.shutdownCalls.Load(); calls != 0 {
				t.Fatalf("SnapshotForShutdown calls = %d, want 0 for a session the remote side owns", calls)
			}
			select {
			case <-completion.done:
			default:
				t.Fatal("shutdown completion was not acknowledged")
			}
		})
	}
}

// TestTUIShutdownSignalDeferredWhileReclaiming keeps the in-flight guard: a
// signal during the handoff transaction must not race the manager's final
// snapshot, but it is honored as soon as the reclaim callback lands.
func TestTUIShutdownSignalDeferredWhileReclaiming(t *testing.T) {
	ctrl := &shutdownSnapshotSpy{}
	m := newTestChatTUI()
	m.ctrl = ctrl
	m.takeover = newCLITakeoverManager(nil, nil)
	m.takeover.reclaiming.Store(true)
	completion := newTUIShutdownCompletion()

	next, cmd := m.update(tuiShutdownMsg{completion: completion})
	if cmd != nil {
		t.Fatalf("shutdown during reclaim returned %T, want no quit command", cmd)
	}
	got := next.(chatTUI)
	if !got.takeover.Reclaiming() {
		t.Fatal("reclaim marker was cleared by shutdown race")
	}
	if !got.shutdownAfterReclaim {
		t.Fatal("signal during reclaim was dropped instead of deferred")
	}
	select {
	case <-completion.done:
	default:
		t.Fatal("shutdown during reclaim was not acknowledged")
	}
	if calls := ctrl.shutdownCalls.Load(); calls != 0 {
		t.Fatalf("SnapshotForShutdown calls = %d during reclaim, want 0", calls)
	}

	// The handoff completes and its callback arrives: the deferred exit fires
	// without snapshotting the session the remote side now owns.
	got.takeover.reclaiming.Store(false)
	got.takeover.returned.Store(true)
	next, cmd = got.update(tuiSessionReclaimedMsg{})
	if cmd == nil || cmd() != (tea.QuitMsg{}) {
		t.Fatal("deferred signal shutdown did not quit after the reclaim callback")
	}
	if !next.(chatTUI).sessionReclaimed {
		t.Fatal("reclaim callback did not mark the session reclaimed before quitting")
	}
	if calls := ctrl.shutdownCalls.Load(); calls != 0 {
		t.Fatalf("SnapshotForShutdown calls = %d after reclaim, want 0", calls)
	}
}

func TestBubbleTeaKeepsRunningWhenShutdownRacesReclaim(t *testing.T) {
	m := newTestChatTUI()
	m.takeover = newCLITakeoverManager(nil, nil)
	m.takeover.reclaiming.Store(true)
	p := tea.NewProgram(
		shutdownOnlyProgramModel{chatTUI: m},
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignals(),
	)

	type result struct {
		model tea.Model
		err   error
	}
	done := make(chan result, 1)
	go func() {
		model, err := p.Run()
		done <- result{model: model, err: err}
	}()
	assertRunning := func(stage string) {
		t.Helper()
		select {
		case result := <-done:
			t.Fatalf("shutdown ended Bubble Tea %s: model=%T err=%v", stage, result.model, result.err)
		case <-time.After(100 * time.Millisecond):
		}
	}

	// A signal that races the handoff transaction must not tear the program
	// down underneath the manager's final snapshot...
	p.Send(tuiShutdownMsg{})
	assertRunning("while reclaiming")
	// ...but it is a real exit request: once the reclaim callback lands the
	// program leaves gracefully instead of lingering as an orphan.
	m.takeover.returned.Store(true)
	m.takeover.reclaiming.Store(false)
	p.Send(tuiSessionReclaimedMsg{})
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("deferred shutdown error = %v", result.err)
		}
	case <-time.After(time.Second):
		p.Kill()
		t.Fatal("Bubble Tea did not honor the signal deferred across the reclaim")
	}
}

// shutdownOnlyProgramModel suppresses chatTUI's unrelated rendering and Init
// work while delegating messages to the production shutdown handler.
type shutdownOnlyProgramModel struct{ chatTUI }

func (shutdownOnlyProgramModel) Init() tea.Cmd { return nil }

func (m shutdownOnlyProgramModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	return shutdownOnlyProgramModel{chatTUI: next.(chatTUI)}, cmd
}

func (shutdownOnlyProgramModel) View() tea.View { return tea.NewView("") }

func TestWatchdogDoesNotReclassifyCompletedBubbleTeaShutdownAsKilled(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	ctrl := &shutdownSnapshotSpy{started: started, release: release}
	m := newTestChatTUI()
	m.ctrl = ctrl
	p := tea.NewProgram(
		shutdownOnlyProgramModel{chatTUI: m},
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignals(),
	)

	type runResult struct {
		model tea.Model
		err   error
	}
	runDone := make(chan runResult, 1)
	go func() {
		model, err := p.Run()
		runDone <- runResult{model: model, err: err}
	}()

	scheduled := make(chan func(), 1)
	completionSeen := make(chan *tuiShutdownCompletion, 1)
	var kills atomic.Int32
	d := &tuiDiagnostics{
		afterFunc: func(delay time.Duration, fn func()) {
			if delay != watchdogKillFallbackDelay {
				t.Errorf("fallback delay = %s, want %s", delay, watchdogKillFallbackDelay)
			}
			scheduled <- fn
		},
		shutdownFn: func(completion *tuiShutdownCompletion) {
			completionSeen <- completion
			p.Send(tuiShutdownMsg{completion: completion})
		},
		killFn: func() {
			kills.Add(1)
			p.Kill()
		},
	}
	killRequestDone := make(chan struct{})
	go func() {
		d.doKill()
		close(killRequestDone)
	}()

	var fallback func()
	select {
	case fallback = <-scheduled:
	case <-time.After(time.Second):
		t.Fatal("watchdog fallback was not armed before shutdown")
	}
	var completion *tuiShutdownCompletion
	select {
	case completion = <-completionSeen:
	case <-time.After(time.Second):
		t.Fatal("watchdog did not send a completion-bearing shutdown message")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Bubble Tea did not enter the final snapshot")
	}
	close(release)
	select {
	case <-completion.done:
	case <-time.After(time.Second):
		t.Fatal("final snapshot completed without acknowledging shutdown")
	}

	// Exercise the old failure window: Update has finished the snapshot, but
	// Bubble Tea may not have consumed tea.Quit yet when the timer callback runs.
	fallback()
	select {
	case <-killRequestDone:
	case <-time.After(time.Second):
		t.Fatal("watchdog shutdown request remained blocked")
	}
	select {
	case result := <-runDone:
		if result.err != nil {
			t.Fatalf("Bubble Tea shutdown error = %v, want graceful nil", result.err)
		}
		if _, ok := result.model.(shutdownOnlyProgramModel); !ok {
			t.Fatalf("final model = %T, want shutdownOnlyProgramModel", result.model)
		}
	case <-time.After(time.Second):
		p.Kill()
		t.Fatal("Bubble Tea program did not exit")
	}
	if got := kills.Load(); got != 0 {
		t.Fatalf("hard-kill calls = %d, want 0 after completed snapshot", got)
	}
}
