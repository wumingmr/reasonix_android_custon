package control

import "reasonix/internal/session"

// turnFinishingBoundary exposes exact execution and TurnDone fan-out
// transitions without making observers poll scheduler-dependent state.
type turnFinishingBoundary struct {
	done     chan struct{}
	idleDone chan struct{}
}

func (b *turnFinishingBoundary) beginIdle() {
	if b.idleDone == nil {
		b.idleDone = make(chan struct{})
	}
}

func (b *turnFinishingBoundary) endIdle() {
	if b.idleDone == nil {
		return
	}
	close(b.idleDone)
	b.idleDone = nil
}

func (b *turnFinishingBoundary) begin(finishing bool) {
	if finishing {
		b.done = make(chan struct{})
	}
}

func (b *turnFinishingBoundary) end() {
	if b.done == nil {
		return
	}
	close(b.done)
	b.done = nil
}

// Running reports whether a turn is currently in flight.
func (c *Controller) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	if c.turns.phase == session.RuntimeRecoveryRequired && c.turns.done != nil {
		return true
	}
	return c.bodyActiveLocked() || c.finalizingLocked() || c.maintenance != nil
}

// TurnIdleDone returns a boundary that closes when the currently admitted turn
// chain releases the running-or-finalizing admission gate. A turn parked during
// TurnDone fan-out remains in the same chain, so the boundary stays open until
// that turn also completes. Idle controllers return ok=false.
func (c *Controller) TurnIdleDone() (done <-chan struct{}, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.turns.finishingBound.idleDone == nil {
		return nil, false
	}
	return c.turns.finishingBound.idleDone, true
}

// TurnFinishingDone returns the current TurnDone delivery boundary.
func (c *Controller) TurnFinishingDone() (done <-chan struct{}, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.finalizingLocked() || c.turns.finishingBound.done == nil {
		return nil, false
	}
	return c.turns.finishingBound.done, true
}
