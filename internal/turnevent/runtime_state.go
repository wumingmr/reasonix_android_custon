package turnevent

import "reasonix/internal/event"

// RuntimeIdentity returns the last committed lifecycle identity and watermark
// under one ledger lock. It performs no replay, compaction or file reads.
func (l *Ledger) RuntimeIdentity() (string, event.TurnStatus, uint64) {
	if l == nil {
		return "", "", 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active, l.status, l.latestLocked()
}

// TodoState returns the last complete replacement committed on the current
// head. The boolean distinguishes an explicit empty replacement from no write.
func (l *Ledger) TodoState() ([]event.Todo, bool) {
	if l == nil {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]event.Todo(nil), l.todos...), l.todoWritten
}

// RecoveryStatus returns the exact durable recovery terminal. Callers must not
// infer its cause from a generic running flag because restart, uncertain tool
// effects, and an expired cancellation grace require different remediation.
func (l *Ledger) RecoveryStatus() *event.RecoveryStatus {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return cloneRecoveryStatus(l.recovery)
}

func cloneRecoveryStatus(in *event.RecoveryStatus) *event.RecoveryStatus {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
