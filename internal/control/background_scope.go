package control

import (
	"reasonix/internal/event"
	"reasonix/internal/jobs"
)

type controllerBackground struct {
	scope     *jobs.SessionBackgroundScope
	recorder  jobs.TaskRecorder
	sink      event.Sink
	release   func()
	rollback  func()
	publish   func()
	candidate bool
	retired   bool
}

func (c *Controller) BackgroundScope() *jobs.SessionBackgroundScope { return c.background.scope }

// ModelReplacementBlocked retains the strict foreground/interaction guards,
// but session-owned processes need not stop for a model-only replacement.
func ModelReplacementBlocked(ctrl interface{ RuntimeStatus() RuntimeStatus }) bool {
	if ctrl == nil {
		return false
	}
	if c, ok := ctrl.(*Controller); ok && c.background.scope != nil {
		c.mu.Lock()
		busy := c.closed || c.bodyActiveLocked() || c.cancelRequestedLocked() || c.maintenance != nil || c.finalizingLocked() || c.recoveryRequiredLocked()
		c.mu.Unlock()
		return busy || c.PendingPrompt() || len(c.jobs.BlockingJobs(c.parentSessionID())) > 0
	}
	status := ctrl.RuntimeStatus()
	return status.Running || status.PendingPrompt || status.BackgroundJobs > 0
}

func (c *Controller) ModelReplacementJobs() []jobs.View {
	if c.background.scope == nil {
		return c.Jobs()
	}
	return c.jobs.BlockingJobs(c.parentSessionID())
}

// StageBackgroundReplacement transfers the reservation to the candidate. It
// remains sealed until publication or candidate disposal, not merely Build.
func (c *Controller) StageBackgroundReplacement(release func()) {
	c.mu.Lock()
	c.background.release = release
	c.mu.Unlock()
}

func (c *Controller) finishBackgroundReplacement(publish bool) {
	c.mu.Lock()
	release := c.background.release
	rollback := c.background.rollback
	publishRuntime := c.background.publish
	c.background.release = nil
	c.background.rollback = nil
	c.background.publish = nil
	c.mu.Unlock()
	if publish && publishRuntime != nil {
		publishRuntime()
	}
	if publish && c.background.scope != nil {
		c.background.scope.Bind(c.background.sink, c.background.recorder)
		c.mu.Lock()
		c.background.candidate = false
		c.mu.Unlock()
	}
	if !publish && rollback != nil {
		rollback()
	}
	if release != nil {
		release()
	}
}

func (c *Controller) PublishBackgroundScope() { c.finishBackgroundReplacement(true) }

func (c *Controller) StageReplacementRollback(rollback func()) {
	c.mu.Lock()
	c.background.rollback = rollback
	c.mu.Unlock()
}

// StageReplacementPublication commits prepared, nonblocking runtime metadata
// only after execution ownership transfers. It must not perform I/O or invoke
// external callbacks while the host's compare-and-publish lock is held.
func (c *Controller) StageReplacementPublication(publish func()) {
	c.mu.Lock()
	c.background.publish = publish
	c.mu.Unlock()
}

func (c *Controller) isBackgroundCandidate() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.background.candidate
}

func (c *Controller) receivesBackgroundRuntimeEvents() bool {
	c.mu.Lock()
	inactive := c.background.candidate || c.background.retired || c.closed
	c.mu.Unlock()
	return !inactive && (c.background.scope == nil || !c.background.scope.Manager.ReplacementInProgress())
}
