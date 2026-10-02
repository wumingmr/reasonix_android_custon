package control

import (
	"context"
	"errors"

	"reasonix/internal/event"
	"reasonix/internal/session"
)

// runSynchronousTurn owns the blocking transport lifecycle. Durable steer
// acknowledgement is intentionally shared with the asynchronous completion
// path, while follow-up dispatch remains owned by the synchronous frontend so
// its response sink stays bound for every queued turn.
func (c *Controller) runSynchronousTurn(
	ctx context.Context,
	onAdmitted func() error,
	run func(context.Context) error,
) error {
	releaseAdmission := c.trySubmissionAdmissionLock()
	if releaseAdmission == nil {
		return ErrTurnRunning
	}
	defer releaseAdmission()
	if err := c.authentication.admissionError(); err != nil {
		return err
	}
	if err := c.ensureWriteAuthorityReady(); err != nil {
		return err
	}
	if ledger := c.turnEventLedger(); ledger != nil && ledger.CurrentStatus() == event.TurnRecoveryRequired {
		return ErrRecoveryRequired
	}
	parent := ctx
	c.mu.Lock()
	// Finishing is part of the gate: TurnDone is still fanning out. Closed
	// seals a torn-down controller. Blocking callers get an error rather than
	// parking because they already own and enforce the request boundary.
	if c.maintenance != nil {
		err := ErrMaintenanceBusy
		if c.maintenance.activity == "recovery_required" {
			err = ErrMaintenanceRecovery
		}
		c.mu.Unlock()
		return err
	}
	if c.bodyActiveLocked() || c.finalizingLocked() || c.rotating || c.closed || c.recoveryRequiredLocked() {
		c.mu.Unlock()
		return ErrTurnRunning
	}
	if c.rejectDrainingGenerationLocked() {
		c.mu.Unlock()
		c.emitDrainingNotice()
		return ErrRuntimeDraining
	}
	ctx, cancel, admitted := c.startTurnLocked(ctx, queuedTurn{})
	if !admitted {
		c.mu.Unlock()
		c.emitDrainingNotice()
		return ErrRuntimeDraining
	}
	c.mu.Unlock()
	if parent != nil {
		stop := context.AfterFunc(parent, func() { c.signalTurnCancel() })
		defer stop()
	}
	c.refreshRuntimeState(event.Event{})
	finish := func() { c.finishSynchronousTurn(cancel) }
	if onAdmitted != nil {
		if err := onAdmitted(); err != nil {
			finish()
			return err
		}
	}
	defer event.RecordTurnCompletion(c.sink)
	defer func() {
		finish()
		c.onInboxTurnDone()
	}()
	// Blocking transports use the same host-owned turn boundary as interactive
	// submissions. The agent's TurnStarted notification is deliberately a
	// duplicate display event; it cannot create runtime ownership or a durable
	// turn by itself.
	run = c.prepareTurnAdmission(run)
	releaseAdmission()
	runErr := run(ctx)
	c.authentication.recordFailure(runErr, c.ModelRef())
	// Keep the execution binding through the synchronous terminal commit just
	// like the asynchronous loop. Close may make the public controller view
	// closed here, but it cannot release the ledger/session underneath TurnDone.
	c.mu.Lock()
	if c.turns.done != nil {
		close(c.turns.done)
		c.turns.done = nil
	}
	c.turns.cancel = nil
	if c.turns.phase != session.RuntimeRecoveryRequired {
		c.turns.phase = session.RuntimeFinalizing
		c.turns.finishingBound.begin(true)
		c.noteExecutionLocked(session.RuntimeFinalizing, "turn")
	}
	c.mu.Unlock()
	c.refreshRuntimeState(event.Event{})
	if ledger := c.turnEventLedger(); ledger != nil && ledger.ActiveTurnID() != "" && !ledger.CurrentStatus().Terminal() {
		cancelled := errors.Is(ctx.Err(), context.Canceled)
		done := event.Event{Kind: event.TurnDone, Err: runErr, Cancelled: cancelled, Outcome: turnOutcome(runErr)}
		if cancelled {
			done.Status = event.TurnInterrupted
		} else if runErr != nil {
			done.Status = event.TurnFailed
		} else {
			done.Status = event.TurnCompleted
		}
		if terminalErr := c.emitTurnEventChecked(done); terminalErr != nil {
			runErr = errors.Join(runErr, terminalErr)
		}
	}
	return runErr
}

func (c *Controller) finishSynchronousTurn(cancel context.CancelFunc) {
	c.mu.Lock()
	if c.turns.done != nil {
		close(c.turns.done)
		c.turns.done = nil
	}
	c.turns.cancel = nil
	c.turns.cancelRequested = false
	closing := c.closed
	recovery := c.turns.phase == session.RuntimeRecoveryRequired
	c.turns.finishingBound.end()
	c.turns.finishingBound.endIdle()
	if !recovery {
		c.turns.lastToken = c.turns.token
		if closing {
			c.turns.phase = session.RuntimeClosed
		} else {
			c.turns.phase = session.RuntimeIdle
		}
		c.turns.turnID = ""
		c.noteExecutionLocked(session.RuntimeIdle, "")
	}
	c.mu.Unlock()
	if closing {
		c.finalizeControllerClose()
	}
	c.refreshRuntimeState(event.Event{})
	c.kickGoalDriver()
	cancel()
}
