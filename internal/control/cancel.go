package control

import (
	"reasonix/internal/agent"
	"reasonix/internal/event"
)

// CancelReceipt acknowledges a session-scoped Stop request. Accepted means the
// cancellation signal was processed; it does not claim that every owned
// operation has already exited.
type CancelReceipt struct {
	SessionRef       string `json:"sessionRef"`
	HeadID           string `json:"headId"`
	RuntimeEpoch     string `json:"runtimeEpoch"`
	Accepted         bool   `json:"accepted"`
	AlreadyIdle      bool   `json:"alreadyIdle"`
	RecoveryRequired bool   `json:"recoveryRequired"`
}

// CancelSession stops the activity owned by this captured controller. Callers
// do not need a turn id, and an idle cancellation is idempotently successful.
func (c *Controller) CancelSession() CancelReceipt {
	return c.CancelSessionFrom("unknown")
}

// CancelSessionFrom records provenance only; cancellation ownership is unchanged.
func (c *Controller) CancelSessionFrom(source string) CancelReceipt {
	if c == nil {
		return CancelReceipt{Accepted: true, AlreadyIdle: true}
	}
	c.recordLifecycle("cancel_received", source, "", 0, "")
	c.mu.Lock()
	alreadyIdle := c.turns.cancel == nil && !c.bodyActiveLocked() && !c.finalizingLocked() && c.maintenance == nil
	sessionRef := c.sessionPath
	c.mu.Unlock()
	headID := agent.BranchID(sessionRef)
	// Receipt metadata must not wait for a producer that is itself stalled on
	// execution. Stop signals the owner independently of state observation.
	state := c.PublishedRuntimeStateSnapshot()
	_, runtime, exclusive := c.v3Binding()
	if exclusive && runtime != nil {
		sessionRef = runtime.Ref().SessionID
		headID = ""
	}
	maintenanceID, maintenancePresent, _ := c.signalMaintenanceCancel()
	var token uint64
	var turnID string
	var cancelled bool
	if maintenancePresent {
		turnID = maintenanceID
		alreadyIdle = false
	} else {
		token, turnID, cancelled = c.signalTurnCancelIdentity()
	}
	c.recordLifecycle("cancel_requested", source, turnID, 0, "")
	if cancelled {
		alreadyIdle = false
	}
	if !maintenancePresent {
		go c.finishCancellation(token, turnID, cancelled)
	}
	receipt := CancelReceipt{
		SessionRef: sessionRef, HeadID: headID, RuntimeEpoch: state.RuntimeEpoch,
		Accepted: true, AlreadyIdle: alreadyIdle, RecoveryRequired: state.Phase == "recovery_required",
	}
	c.recordLifecycle("cancel_acknowledged", source, turnID, 0, "")
	return receipt
}

// Cancel aborts the in-flight turn. A goroutine blocked awaiting approval
// unblocks via the cancelled context.
func (c *Controller) Cancel() {
	c.recordLifecycle("cancel_requested", "unknown", "", 0, "")
	if _, present, _ := c.signalMaintenanceCancel(); present {
		return
	}
	turnID, cancelled := c.cancelTurnLocked()
	c.finishCancel(turnID, cancelled)
}

// cancelLocked is retained for call sites already inside a typed prompt
// transition. Cancellation itself is independent of answer serialization.
func (c *Controller) cancelLocked() {
	turnID, cancelled := c.cancelTurnLocked()
	c.finishCancel(turnID, cancelled)
}

// cancelTurnLocked signals the turn before any observable work: the status
// emit that follows is a synchronous event barrier, and a stalled event lane
// must never keep the provider stream or a tool process alive after Stop.
func (c *Controller) cancelTurnLocked() (string, bool) {
	_, turnID, cancelled := c.signalTurnCancelIdentity()
	if !cancelled {
		return "", false
	}
	c.promptOwner.CancelTurn(turnID)
	return turnID, true
}

func (c *Controller) finishCancellation(token uint64, turnID string, cancelled bool) {
	c.mu.Lock()
	current := c.turns.token == token
	c.mu.Unlock()
	if !current {
		return
	}
	c.promptOwner.CancelTurn(turnID)
	c.finishCancel(turnID, cancelled)
}

func (c *Controller) finishCancel(turnID string, cancelled bool) {
	defer c.refreshRuntimeState(event.Event{})
	if cancelled {
		c.emitTurnStatus(event.TurnCancelling, turnID)
	}
	c.mu.Lock()
	stale := turnID != "" && c.turns.turnID != "" && c.turns.turnID != turnID
	c.mu.Unlock()
	if stale {
		return
	}
	if c.goals.active() {
		c.stopGoal(GoalStatusStopped)
	}
	if c.sessionEngineEnabled() {
		c.disarmGoalLifecycle("cancelled")
	}
}
