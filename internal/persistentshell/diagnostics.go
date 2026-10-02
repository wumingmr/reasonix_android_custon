package persistentshell

import "sync/atomic"

type shellMetrics struct {
	started           atomic.Uint64
	startupFailed     atomic.Uint64
	completionMissing atomic.Uint64
	timedOut          atomic.Uint64
	reset             atomic.Uint64
}

// Diagnostics is bounded process-local telemetry, with no commands or environment.
func (m *Manager) Diagnostics() map[string]uint64 {
	if m == nil {
		return nil
	}
	return map[string]uint64{"started": m.metrics.started.Load(), "startupFailed": m.metrics.startupFailed.Load(),
		"completionMissing": m.metrics.completionMissing.Load(), "timedOut": m.metrics.timedOut.Load(), "reset": m.metrics.reset.Load()}
}
