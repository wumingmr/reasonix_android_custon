package jobs

import (
	"context"
	"time"
)

// Close cancels the session context and waits briefly for every background job
// goroutine to return before unblocking. If a non-cooperative job ignores
// cancellation, cleanup of the temporary artifact root continues in the
// background after the goroutines eventually unwind.
func (m *Manager) Close() {
	_ = m.CloseWithGrace(m.teardownGrace)
}

// CloseAsync cancels the manager and returns immediately. It is used when a
// caller has already begun session-specific teardown and owns the delayed
// persistent cleanup, but still needs the manager's root context and temporary
// artifact root released eventually.
func (m *Manager) CloseAsync() {
	m.mu.Lock()
	m.cancel()
	m.mu.Unlock()
	go func() {
		m.wg.Wait()
		m.releaseOwner()
		m.removeTempRoot()
	}()
}

// CloseWithGrace is Close with an explicit wait window, used by tests and
// callers that need to surface non-cooperative jobs.
func (m *Manager) CloseWithGrace(grace time.Duration) TeardownResult {
	m.mu.Lock()
	m.cancel()
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		m.releaseOwner()
		close(done)
	}()
	result, timedOut := waitTeardownTargets(context.Background(), m.closeTargets(), grace, done)
	if timedOut {
		m.emitTeardownTimeout("close", result)
		go func() {
			<-done
			m.removeTempRoot()
		}()
		return result
	}
	m.removeTempRoot()
	return result
}
