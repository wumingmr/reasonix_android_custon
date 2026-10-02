package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"reasonix/desktop/internal/sessionui"
	"reasonix/internal/identitylock"
)

var errManualCreationIdentity = errors.New("invalid manual creation identity or state")

func decodeManualCreation(r sessionui.Record) (ManualSessionCreationView, error) {
	var v ManualSessionCreationView
	if err := json.Unmarshal(r.Payload, &v); err != nil {
		return v, err
	}
	sum := sha256.Sum256([]byte(r.Key))
	if v.OperationID != r.Key || v.WorkspaceID == "" || v.Ref.HostID != localDesktopHostID ||
		v.Ref.SessionID != fmt.Sprintf("desktop-manual-%x", sum[:16]) || v.TopicID != fmt.Sprintf("manual-%x", sum[:16]) {
		return v, errManualCreationIdentity
	}
	switch v.Phase {
	case "reserved", "starting", "ready", "failed":
	default:
		return v, errManualCreationIdentity
	}
	return v, nil
}

func manualCreationBackoff(n int) time.Duration {
	base := 500 * time.Millisecond * time.Duration(1<<min(n, 4))
	// Small downward jitter preserves the five-second upper bound.
	return min(base, 5*time.Second) * time.Duration(900+rand.IntN(101)) / 1000
}

func manualCreationRetryable(err error) bool {
	if errors.Is(err, sessionui.ErrConflict) {
		return true
	}
	var sqlErr interface{ Code() int }
	if errors.As(err, &sqlErr) {
		switch sqlErr.Code() & 255 {
		case 5, 6, 10:
			return true
		} // BUSY, LOCKED, IOERR
	}
	return false
}

// Target availability can be retried with the same identity. Result-save retries
// retain the lock and the computed result, never re-entering the runtime builder.
func (m *manualCreationManager) attempt(t *manualCreationTask) (bool, error) {
	r, err := m.store.Get(m.ctx, "creation", t.id)
	if err != nil {
		m.setStage(t, "retrying_storage", "queued")
		return manualCreationRetryable(err), err
	}
	v, err := decodeManualCreation(r)
	if err != nil {
		return false, err
	}
	m.mu.Lock()
	t.sessionID, t.revision = v.Ref.SessionID, r.Revision
	m.mu.Unlock()
	if v.Phase == "ready" || (v.Phase == "failed" && !legacyManualCreationConflict(v) && !m.mayRetryFailure(t, r.Revision)) {
		return false, nil
	}
	lockRoot := filepath.Join(filepath.Dir(m.a.sessionUIStore().Path()), "manual-creation-locks")
	if err := os.MkdirAll(lockRoot, 0700); err != nil {
		return false, err
	}
	release, err := identitylock.TryAcquire(filepath.Join(lockRoot, v.Ref.SessionID+".lock"))
	if errors.Is(err, identitylock.ErrHeld) {
		m.setStage(t, "waiting_lock", "waiting_lock")
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer release()
	current, err := m.store.Get(m.ctx, "creation", t.id)
	if err != nil {
		m.setStage(t, "retrying_storage", "preparing_storage")
		return manualCreationRetryable(err), err
	}
	fresh, err := decodeManualCreation(current)
	if err != nil || fresh.Ref != v.Ref || fresh.WorkspaceID != v.WorkspaceID {
		return false, errManualCreationIdentity
	}
	if fresh.Phase == "ready" || (fresh.Phase == "failed" && !legacyManualCreationConflict(fresh) && !m.mayRetryFailure(t, current.Revision)) {
		return false, nil
	}
	if err := m.ctx.Err(); err != nil {
		return false, err
	}
	r, v = current, fresh
	r, err = m.savePhase(r, "starting", "")
	if err != nil {
		m.setStage(t, "retrying_storage", "queued")
		return manualCreationRetryable(err), err
	}
	m.mu.Lock()
	t.revision = r.Revision
	m.mu.Unlock()
	err = m.execute(m.ctx, v, func(stage string) { m.setStage(t, "running", stage) })
	// Shutdown interruption is recoverable on the next start, not a user failure.
	if m.ctx.Err() != nil || m.a.shuttingDown.Load() {
		return false, context.Canceled
	}
	var targetErr *SessionOperationError
	if errors.As(err, &targetErr) && targetErr.Code == "workspace_unavailable" {
		m.setStage(t, "waiting_workspace", "preparing_storage")
		return true, nil
	}
	phase, message := "ready", ""
	if err != nil {
		phase, message = "failed", sessionOperationErrorForTarget(err, v.Ref.SessionID, v.OperationID).Error()
	}
	m.setStage(t, "running", "persisting_result")
	err = m.persistResult(t, r, v, phase, message)
	if err == nil {
		m.a.emitProjectTreeChanged()
	}
	return false, err
}

func (m *manualCreationManager) savePhase(r sessionui.Record, phase, message string) (sessionui.Record, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(r.Payload, &fields); err != nil {
		return r, err
	}
	fields["phase"], _ = json.Marshal(phase)
	delete(fields, "error")
	if message != "" {
		fields["error"], _ = json.Marshal(message)
	}
	// Only phase/error are replaced; nested and unknown fields stay opaque.
	payload, err := json.Marshal(fields)
	if err != nil {
		return r, err
	}
	return m.store.Save(m.ctx, "creation", r.Key, r.Revision, payload)
}

func (m *manualCreationManager) persistResult(t *manualCreationTask, r sessionui.Record, identity ManualSessionCreationView, phase, message string) error {
	for n := 0; ; n++ {
		if err := m.ctx.Err(); err != nil {
			return err
		}
		saved, err := m.savePhase(r, phase, message)
		if err == nil {
			m.mu.Lock()
			t.revision = saved.Revision
			m.mu.Unlock()
			return nil
		}
		if !manualCreationRetryable(err) {
			return err
		}
		m.setStage(t, "retrying_storage", "persisting_result")
		if err := m.waitStorageRetry(t, n); err != nil {
			return err
		}
		// Retry reads as well as writes without ever discarding the runtime result.
		for {
			r, err = m.store.Get(m.ctx, "creation", t.id)
			if err == nil {
				break
			}
			if !manualCreationRetryable(err) {
				return err
			}
			if err = m.waitStorageRetry(t, n); err != nil {
				return err
			}
		}
		v, err := decodeManualCreation(r)
		if err != nil || v.Ref != identity.Ref || v.WorkspaceID != identity.WorkspaceID {
			return errManualCreationIdentity
		}
		if v.Phase == phase {
			m.mu.Lock()
			t.revision = r.Revision
			m.mu.Unlock()
			return nil
		}
		if v.Phase != "starting" {
			return errManualCreationIdentity
		}
		m.mu.Lock()
		t.revision = r.Revision
		m.mu.Unlock()
	}
}

func (m *manualCreationManager) waitStorageRetry(t *manualCreationTask, n int) error {
	delay := manualCreationBackoff(n)
	m.mu.Lock()
	t.progress.NextRetryAt = m.now().Add(delay).UnixMilli()
	m.mu.Unlock()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-m.ctx.Done():
		return m.ctx.Err()
	case <-timer.C:
		return nil
	}
}
