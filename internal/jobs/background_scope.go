package jobs

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/workspacelease"
)

// SessionBackgroundScope owns resources which outlive a controller generation.
// Build candidates acquire a reference before borrowing them; failed candidates
// release only that reference. Jobs do not retain the scope themselves.
type SessionBackgroundScope struct {
	Manager        *Manager
	WorkspaceLease *workspacelease.Owner
	mu             sync.Mutex
	refs           int
	closed         bool
}

func NewSessionBackgroundScope(manager *Manager, lease *workspacelease.Owner) *SessionBackgroundScope {
	return &SessionBackgroundScope{Manager: manager, WorkspaceLease: lease, refs: 1}
}

func (s *SessionBackgroundScope) Acquire() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("background scope is closed")
	}
	s.refs++
	return nil
}

func (s *SessionBackgroundScope) Release(async bool) {
	s.mu.Lock()
	if s.refs == 0 {
		s.mu.Unlock()
		return
	}
	s.refs--
	closeNow := s.refs == 0
	if closeNow {
		s.closed = true
	}
	s.mu.Unlock()
	if closeNow {
		if async {
			s.Manager.CloseAsync()
		} else {
			s.Manager.Close()
		}
	}
}

// Bind is called only on publication, never while staging a replacement.
func (s *SessionBackgroundScope) Bind(sink event.Sink, recorder TaskRecorder) {
	s.Manager.bindingMu.Lock()
	s.Manager.sink = sink
	if s.Manager.taskRecorder == nil {
		s.Manager.taskRecorder = recorder
	}
	s.Manager.bindingMu.Unlock()
}

type Lifetime string

const (
	RuntimeBound   Lifetime = "runtime_bound"
	SessionProcess Lifetime = "session_process"
)

var ErrRebuildInProgress = errors.New("background task admission is paused for model configuration replacement")

// BeginReplacement checks and seals task registration under the same lock.
// Completion and cancellation remain available throughout the reservation.
func (m *Manager) BeginReplacement(session string) (func(), error) {
	started := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.replacing {
		return nil, ErrRebuildInProgress
	}
	if m.root.Err() != nil {
		return nil, errors.New("background scope is closed")
	}
	if len(m.blockingJobsLocked(session)) > 0 {
		return nil, errors.New("runtime-dependent background jobs are still running")
	}
	m.replacing = true
	m.eventMu.Lock()
	m.eventPaused = true
	m.eventMu.Unlock()
	return sync.OnceFunc(func() {
		m.mu.Lock()
		m.replacing = false
		m.mu.Unlock()
		m.eventMu.Lock()
		m.eventPaused = false
		m.eventMu.Unlock()
		go func() {
			m.drainEvents()
			// Observers suppress candidate/old-generation callbacks while sealed.
			// Resample after publication or rollback even if the final process
			// exited during the build and no later job transition will occur.
			m.notifyRuntime("", "")
			slog.Debug("model replacement background reservation released", "phase", "release", "task_class", SessionProcess, "duration", time.Since(started))
		}()
	}), nil
}

func (m *Manager) BlockingJobs(session string) []View {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.blockingJobsLocked(session)
}

func (m *Manager) blockingJobsLocked(session string) []View {
	out := []View{}
	for _, key := range m.order {
		j := m.jobs[key]
		if !sessionMatches(session, j.SessionID) || j.lifetime == SessionProcess {
			continue
		}
		select {
		case <-j.done:
			continue
		default:
		}
		j.mu.Lock()
		out = append(out, View{ID: j.ID, Kind: j.Kind, Label: j.Label, Status: string(Running), StartedAt: j.clock.startedAt})
		j.mu.Unlock()
	}
	return out
}

func (m *Manager) boundSink() event.Sink {
	return m
}

// Emit queues lifecycle notices across replacement. Only one drainer invokes
// the current sink, always outside registry/binding locks.
func (m *Manager) Emit(e event.Event) {
	m.eventMu.Lock()
	m.eventQueue = append(m.eventQueue, e)
	m.eventMu.Unlock()
	m.drainEvents()
}

func (m *Manager) drainEvents() {
	m.eventMu.Lock()
	if m.eventPaused || m.eventDraining {
		m.eventMu.Unlock()
		return
	}
	m.eventDraining = true
	for len(m.eventQueue) > 0 && !m.eventPaused {
		e := m.eventQueue[0]
		m.eventQueue[0] = event.Event{}
		m.eventQueue = m.eventQueue[1:]
		m.eventMu.Unlock()
		m.bindingMu.RLock()
		sink := m.sink
		m.bindingMu.RUnlock()
		if sink != nil {
			sink.Emit(e)
		}
		m.eventMu.Lock()
	}
	m.eventDraining = false
	m.eventMu.Unlock()
}

func (m *Manager) boundRecorder() TaskRecorder {
	m.bindingMu.RLock()
	defer m.bindingMu.RUnlock()
	return m.taskRecorder
}

func (m *Manager) ActiveSessionID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

func (m *Manager) ReplacementInProgress() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.replacing
}
