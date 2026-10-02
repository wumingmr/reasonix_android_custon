package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"reasonix/desktop/internal/sessionui"
)

type manualCreationStore interface {
	Get(context.Context, string, string) (sessionui.Record, error)
	List(context.Context, string) ([]sessionui.Record, error)
	Save(context.Context, string, string, string, json.RawMessage, ...sessionui.Record) (sessionui.Record, error)
}

type manualCreationTask struct {
	id, retryRevision   string
	running, counted    bool
	wakeRequested       bool
	next                time.Time
	retries             int
	progress            ManualCreationProgress
	sessionID, revision string
	attempt, generation uint64
	lastSlowLog         time.Time
}

// One scheduler owns admission and delayed attempts. The OS lock remains the
// cross-process authority; neither progress nor elapsed time grants ownership.
type manualCreationManager struct {
	mu                sync.Mutex
	tasks             map[string]*manualCreationTask
	events            []manualCreationDiagnostic
	ctx               context.Context
	cancel            context.CancelFunc
	wake, done        chan struct{}
	stopped, scanning bool
	nextScan          time.Time
	a                 *App
	store             manualCreationStore
	now               func() time.Time
	execute           func(context.Context, ManualSessionCreationView, func(string)) error
}

func (a *App) creationManager() *manualCreationManager {
	a.manualCreationMu.Lock()
	defer a.manualCreationMu.Unlock()
	if a.manualCreations == nil {
		ctx, cancel := context.WithCancel(a.bootContext())
		m := &manualCreationManager{a: a, ctx: ctx, cancel: cancel, tasks: make(map[string]*manualCreationTask),
			wake: make(chan struct{}, 1), done: make(chan struct{}), store: a.sessionUIStore(), now: time.Now,
			execute: a.createManualSessionRuntime}
		m.stopped = a.shuttingDown.Load()
		if m.stopped {
			cancel()
		}
		a.manualCreations = m
		a.goSafe("manualCreationScheduler", m.loop)
	}
	return a.manualCreations
}

// retryRevision is populated only by an explicit retry of the observed failed
// record. A delayed request cannot restart a newer failure.
func (m *manualCreationManager) Ensure(id, reason, retryRevision string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped || m.ctx.Err() != nil || m.a.shuttingDown.Load() {
		return
	}
	t := m.tasks[id]
	if t != nil {
		if t.running {
			if reason == "retry" {
				t.retryRevision = retryRevision
				t.wakeRequested = true
			}
			return
		}
		if t.progress.Status == "blocked" && reason != "retry" {
			return
		}
	}
	if t == nil {
		t = &manualCreationTask{id: id}
		m.tasks[id] = t
	}
	if reason == "retry" {
		t.retryRevision = retryRevision
	}
	if !t.counted {
		m.a.manualCreationTasks.Add(1)
		t.counted = true
	}
	if t.next.IsZero() || reason == "retry" {
		t.next = m.now()
		t.progress = ManualCreationProgress{Status: "queued", Stage: "queued", StageStartedAt: t.next.UnixMilli()}
	}
	m.notify()
}

func (m *manualCreationManager) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *manualCreationManager) armRecovery() {
	m.mu.Lock()
	if !m.stopped {
		m.scanning = true
		m.nextScan = m.now()
	}
	m.mu.Unlock()
	m.notify()
}

func (m *manualCreationManager) StopAdmission() {
	m.mu.Lock()
	m.stopped = true
	m.scanning = false
	m.mu.Unlock()
	m.notify()
}

func (m *manualCreationManager) CancelAndWait(ctx context.Context) error {
	m.StopAdmission()
	m.cancel()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *manualCreationManager) loop() {
	defer close(m.done)
	for {
		if m.ctx.Err() != nil {
			m.mu.Lock()
			for id, t := range m.tasks {
				if !t.running {
					m.finishLocked(t)
					delete(m.tasks, id)
				}
			}
			m.mu.Unlock()
			m.a.manualCreationTasks.Wait()
			return
		}
		m.mu.Lock()
		now := m.now()
		scan := !m.stopped && m.scanning && !now.Before(m.nextScan)
		if scan {
			m.nextScan = now.Add(30 * time.Second)
		}
		delay := time.Second // also drives rate-limited slow-stage diagnostics
		for _, t := range m.tasks {
			if m.stopped || t.running || !t.counted {
				continue
			}
			if now.Before(t.next) {
				if d := t.next.Sub(now); d < delay {
					delay = d
				}
				continue
			}
			t.running = true
			t.attempt++
			t.generation = 0
			m.a.goSafe("manualCreationAttempt", func() { m.run(t) })
		}
		m.mu.Unlock()
		m.logSlowStages(now)
		if scan {
			m.scan()
		}
		timer := time.NewTimer(delay)
		select {
		case <-m.ctx.Done():
		case <-m.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (m *manualCreationManager) finishLocked(t *manualCreationTask) {
	if t.counted {
		t.counted = false
		m.a.manualCreationTasks.Done()
	}
}

func (m *manualCreationManager) run(t *manualCreationTask) {
	retry, err := m.attempt(t)
	m.mu.Lock()
	t.running = false
	if m.ctx.Err() == nil && !m.stopped && t.wakeRequested && (retry || err == nil) {
		t.wakeRequested = false
		t.next = m.now()
		t.retries = 0
	} else if m.ctx.Err() == nil && !m.stopped && retry {
		t.next = m.now().Add(manualCreationBackoff(t.retries))
		t.retries++
		t.progress.NextRetryAt = t.next.UnixMilli()
	} else {
		if err != nil && m.ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			t.progress.Status, t.progress.ErrorCode = "blocked", manualCreationErrorCode(err)
		} else {
			t.progress.Status = "completed"
			if m.ctx.Err() != nil || errors.Is(err, context.Canceled) {
				t.progress.Status = "stopped"
			}
			delete(m.tasks, t.id)
		}
		m.recordEventLocked(t)
		m.finishLocked(t)
	}
	attrs := m.logAttrsLocked(t)
	attrs = append(attrs, "error_code", t.progress.ErrorCode)
	m.mu.Unlock()
	slog.Info("desktop: manual creation attempt finished", attrs...)
	m.notify()
}

func (m *manualCreationManager) mayRetryFailure(t *manualCreationTask, revision string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return t.retryRevision == revision
}
