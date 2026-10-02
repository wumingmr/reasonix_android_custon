package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sort"
	"time"

	"reasonix/desktop/internal/sessionui"
)

// ManualCreationProgress is local observational state, never a lease or a
// persisted phase. Missing/unknown fields are safe for older clients.
type ManualCreationProgress struct {
	Status         string `json:"status"`
	Stage          string `json:"stage"`
	StageStartedAt int64  `json:"stageStartedAt"`
	ElapsedMs      int64  `json:"elapsedMs"`
	NextRetryAt    int64  `json:"nextRetryAt,omitempty"`
	ErrorCode      string `json:"errorCode,omitempty"`
	Slow           bool   `json:"slow"`
}

func manualCreationErrorCode(err error) string {
	if errors.Is(err, sessionui.ErrFutureVersion) {
		return "unsupported_ui_schema"
	}
	if errors.Is(err, errManualCreationIdentity) {
		return "creation_state_conflict"
	}
	return "creation_storage_unavailable"
}

func (m *manualCreationManager) Snapshot(id string) *ManualCreationProgress {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.tasks[id]; t != nil {
		p := m.progressLocked(t)
		return &p
	}
	return nil
}

func (m *manualCreationManager) progressLocked(t *manualCreationTask) ManualCreationProgress {
	p := t.progress
	p.ElapsedMs = max(0, m.now().UnixMilli()-p.StageStartedAt)
	p.Slow = p.ElapsedMs >= 30000
	return p
}

func (a *App) creationView(v ManualSessionCreationView) ManualSessionCreationView {
	v.Progress = nil
	v.SurfaceReady = false
	a.mu.RLock()
	for _, tab := range a.tabs {
		if !tab.removed && tab.SessionID == v.Ref.SessionID && v.Ref.SessionID != "" {
			v.SurfaceReady = true
			break
		}
	}
	a.mu.RUnlock()
	a.manualCreationMu.Lock()
	m := a.manualCreations
	a.manualCreationMu.Unlock()
	if m != nil {
		v.Progress = m.Snapshot(v.OperationID)
	}
	return v
}

func (m *manualCreationManager) setStage(t *manualCreationTask, status, stage string) {
	m.setStageForGeneration(t, status, stage, 0)
}

func (m *manualCreationManager) setStageForGeneration(t *manualCreationTask, status, stage string, generation uint64) {
	if stage == "stopping" {
		status = "stopping"
	}
	m.mu.Lock()
	// A late stage callback must never alter a newer task for the same identity.
	if m.tasks[t.id] != t || !t.running {
		m.mu.Unlock()
		return
	}
	if generation != 0 {
		if generation < t.generation {
			m.mu.Unlock()
			return
		}
		t.generation = generation
	}
	now := m.now()
	changed := t.progress.Stage != stage || t.progress.Status != status
	if t.progress.Stage != stage {
		t.progress.StageStartedAt = now.UnixMilli()
		t.lastSlowLog = time.Time{}
	}
	t.progress.Status, t.progress.Stage, t.progress.NextRetryAt = status, stage, 0
	attrs := m.logAttrsLocked(t)
	if changed {
		m.recordEventLocked(t)
	}
	m.mu.Unlock()
	if changed {
		slog.Info("desktop: manual creation stage", attrs...)
	}
}

func (m *manualCreationManager) logAttrsLocked(t *manualCreationTask) []any {
	return []any{"operation", t.id, "session", t.sessionID, "pid", os.Getpid(), "attempt", t.attempt,
		"generation", t.generation, "revision", t.revision, "stage", t.progress.Stage,
		"status", t.progress.Status, "elapsed_ms", max(0, m.now().UnixMilli()-t.progress.StageStartedAt)}
}

func (m *manualCreationManager) logSlowStages(now time.Time) {
	m.mu.Lock()
	var reports [][]any
	for _, t := range m.tasks {
		if t.counted && now.UnixMilli()-t.progress.StageStartedAt >= 30000 && (t.lastSlowLog.IsZero() || now.Sub(t.lastSlowLog) >= time.Minute) {
			t.lastSlowLog = now
			reports = append(reports, m.logAttrsLocked(t))
		}
	}
	m.mu.Unlock()
	for _, attrs := range reports {
		slog.Warn("desktop: manual creation slow stage", attrs...)
	}
}

func (m *manualCreationManager) scan() {
	rows, err := m.store.List(m.ctx, "creation")
	if err != nil {
		slog.Warn("desktop: pending creation scan unavailable", "code", manualCreationErrorCode(err))
		return
	}
	for _, r := range rows {
		var v ManualSessionCreationView
		if json.Unmarshal(r.Payload, &v) != nil {
			m.Ensure(r.Key, "recovery", "")
			continue
		}
		if (v.Phase != "ready" && v.Phase != "failed") || legacyManualCreationConflict(v) {
			m.Ensure(r.Key, "recovery", "")
		}
	}
}

// Content-free local diagnostics can be exported even if the database is busy.
type manualCreationDiagnostic struct {
	OperationID string                 `json:"operationId"`
	SessionID   string                 `json:"sessionId"`
	PID         int                    `json:"pid"`
	Attempt     uint64                 `json:"attempt"`
	Generation  uint64                 `json:"generation"`
	Revision    string                 `json:"revision"`
	Progress    ManualCreationProgress `json:"progress"`
}

func (m *manualCreationManager) recordEventLocked(t *manualCreationTask) {
	m.events = append(m.events, manualCreationDiagnostic{t.id, t.sessionID, os.Getpid(), t.attempt, t.generation, t.revision, m.progressLocked(t)})
	if len(m.events) > 256 {
		m.events = append([]manualCreationDiagnostic(nil), m.events[len(m.events)-256:]...)
	}
}

func (a *App) manualCreationDiagnostics() []manualCreationDiagnostic {
	rows := []manualCreationDiagnostic{}
	a.manualCreationMu.Lock()
	m := a.manualCreations
	a.manualCreationMu.Unlock()
	if m == nil {
		return rows
	}
	m.mu.Lock()
	for _, t := range m.tasks {
		rows = append(rows, manualCreationDiagnostic{t.id, t.sessionID, os.Getpid(), t.attempt, t.generation, t.revision, m.progressLocked(t)})
	}
	m.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].OperationID < rows[j].OperationID })
	return rows
}
