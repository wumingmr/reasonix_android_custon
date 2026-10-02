package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/extension"
	"reasonix/internal/session"
)

var (
	ErrMaintenanceBusy     = fmt.Errorf("%w: session maintenance is already running", ErrTurnRunning)
	ErrMaintenanceRecovery = fmt.Errorf("%w: session maintenance requires recovery", ErrRecoveryRequired)
)

type controllerMaintenance struct {
	maintenanceIdentity
	maintenanceResult
	publishMu       sync.Mutex
	activity        string
	revision        uint64
	cancel          context.CancelFunc
	done            chan struct{}
	safeToRelease   bool
	cancelSignalled bool
	terminal        bool
}

// Identity and the pre-operation checkpoint are immutable after admission.
type maintenanceIdentity struct {
	id                string
	kind              string
	runtimeEpoch      string
	sessionPath       string
	projectionVersion uint64
}

// Result fields are frozen together at each serialized publication boundary.
type maintenanceResult struct {
	status       string
	errorCode    string
	detail       string
	inputTokens  int
	applied      bool
	resultTokens int
	messages     int
	summary      string
	archive      string
}

func (c *Controller) beginMaintenance(parent context.Context, kind string) (*controllerMaintenance, context.Context, error) {
	if parent == nil {
		parent = context.Background()
	}
	if err := c.ensureWriteAuthorityReady(); err != nil {
		return nil, nil, err
	}
	runtimeEpoch := c.RuntimeStateSnapshot().RuntimeEpoch
	c.mu.Lock()
	verb := strings.ReplaceAll(kind, "_", " ")
	if strings.HasPrefix(kind, "summarize_") {
		verb = "summarize"
	}
	switch {
	case c.closed:
		c.mu.Unlock()
		return nil, nil, errors.New("controller is closed")
	case c.bodyActiveLocked() || c.finalizingLocked():
		c.mu.Unlock()
		return nil, nil, fmt.Errorf("cannot %s while a turn is running", verb)
	case c.rotating:
		c.mu.Unlock()
		return nil, nil, errRotationInProgress
	case c.maintenance != nil:
		err := ErrMaintenanceBusy
		if c.maintenance.activity == "recovery_required" {
			err = ErrMaintenanceRecovery
		}
		c.mu.Unlock()
		return nil, nil, err
	case c.recoveryRequiredLocked():
		c.mu.Unlock()
		return nil, nil, ErrMaintenanceRecovery
	}
	if c.rejectDrainingGenerationLocked() {
		c.mu.Unlock()
		return nil, nil, ErrRuntimeDraining
	}
	if c.turns.runtime != nil && !c.turns.runtime.BeginExecution(c.turns.generation, session.MaintenanceActivity) {
		c.mu.Unlock()
		return nil, nil, ErrMaintenanceBusy
	}
	work, cancel := context.WithCancel(extension.ContextWithRuntimeOwner(c.withAuthentication(parent), c.runtimeOwner))
	before := c.ContextMaintenanceSnapshot()
	op := &controllerMaintenance{
		maintenanceIdentity: maintenanceIdentity{
			id: "maintenance-" + newRuntimeStateEpoch(), kind: kind,
			runtimeEpoch: runtimeEpoch, sessionPath: c.sessionPath,
			projectionVersion: before.ProjectionVersion,
		},
		maintenanceResult: maintenanceResult{status: "running", inputTokens: before.ProjectedTokens},
		activity:          "running", cancel: cancel, done: make(chan struct{}),
	}
	c.maintenance = op
	c.mu.Unlock()
	if err := c.emitMaintenanceOperation(op, "running", "", "", before, false); err != nil {
		cancel()
		_ = c.retainMaintenanceRecovery(op, "operation_persist_failed", err, before, false)
		return nil, nil, fmt.Errorf("persist maintenance start: %w", err)
	}
	c.refreshRuntimeState(event.Event{})
	return op, work, nil
}

// startCompactAsync is used by /compact so registration happens before the
// command handler returns. Desktop/HTTP/CLI callers keep the synchronous
// Compact API and wait on the same lifecycle.
func (c *Controller) startCompactAsync(instructions string) error {
	if err := c.authentication.admissionError(); err != nil {
		return err
	}
	if c.executor == nil {
		return nil
	}
	op, ctx, err := c.beginMaintenance(context.Background(), "compact")
	if err != nil {
		return err
	}
	go func() {
		_ = c.executeMaintenance(op, ctx, func(work context.Context) error {
			return c.executor.CompactNow(work, instructions)
		})
	}()
	return nil
}

func (c *Controller) executeMaintenance(op *controllerMaintenance, ctx context.Context, work func(context.Context) error) error {
	err := work(ctx)
	c.authentication.recordFailure(err, c.ModelRef())
	after := c.ContextMaintenanceSnapshot()
	applied := after.ProjectionVersion > op.projectionVersion

	// Once the worker returns, expose the non-cancellable persistence boundary.
	if emitErr := c.emitMaintenanceOperation(op, "finalizing", "", "", after, applied); emitErr != nil {
		return c.retainMaintenanceRecovery(op, "operation_persist_failed", emitErr, after, applied)
	}
	c.refreshRuntimeState(event.Event{})

	status, code, detail := "completed", "", ""
	if err == nil && applied {
		// The final projection commit won the boundary. A Stop that arrived
		// afterwards is idempotent and must not relabel completed work.
	} else if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		status, code = "cancelled", "cancelled"
		if applied {
			status = "partially_completed"
		}
	} else if err != nil {
		status, code, detail = "failed", "summary_failed", err.Error()
	} else if !applied {
		status, code = "noop", "no_history"
	}

	// Manual projection changes and their operation record share one finishing
	// boundary. A save failure retains maintenance ownership and blocks inbox
	// dispatch so the UI cannot report a safe idle session.
	if applied {
		if saveErr := c.SnapshotRewrite(); saveErr != nil {
			return c.retainMaintenanceRecovery(op, "save_failed", saveErr, after, applied)
		}
	}

	if emitErr := c.emitMaintenanceOperation(op, status, code, detail, after, applied); emitErr != nil {
		return c.retainMaintenanceRecovery(op, "operation_persist_failed", emitErr, after, applied)
	}
	c.finishMaintenanceOperation(op)
	return err
}

func (c *Controller) retainMaintenanceRecovery(op *controllerMaintenance, code string, cause error, snapshot agent.ContextMaintenanceSnapshot, applied bool) error {
	if cause == nil {
		cause = ErrMaintenanceRecovery
	}
	// Publication still owns the persistence resources even though the worker
	// has exited. Close may release them only after this final attempt settles.
	_ = c.emitMaintenanceOperation(op, "recovery_required", code, cause.Error(), snapshot, applied)
	c.mu.Lock()
	closed := c.closed
	if c.maintenance == op {
		op.safeToRelease = true
		if closed {
			c.maintenance = nil
			close(op.done)
		}
	}
	c.mu.Unlock()
	c.refreshRuntimeState(event.Event{})
	if closed {
		c.finalizeControllerClose()
	}
	return cause
}

func (c *Controller) emitMaintenanceOperation(op *controllerMaintenance, status, code, detail string, snapshot agent.ContextMaintenanceSnapshot, applied bool) error {
	if op == nil {
		return nil
	}
	op.publishMu.Lock()
	defer op.publishMu.Unlock()
	summary := ""
	if applied && c.executor != nil {
		summary = c.executor.LastCompactionSummary()
	}
	c.mu.Lock()
	if op.terminal {
		c.mu.Unlock()
		return nil
	}
	if c.maintenance != op || !maintenanceTransitionAllowed(op, status, code) {
		c.mu.Unlock()
		return nil
	}
	activity := op.activity
	switch status {
	case "running", "cancelling", "finalizing", "recovery_required":
		activity = status
	}
	info := &event.SessionOperationInfo{
		OperationID: op.id, OperationRevision: op.revision + 1, RuntimeEpoch: op.runtimeEpoch,
		Kind: op.kind, Activity: activity, Status: status,
		ErrorCode: code, Detail: detail, Applied: op.applied || applied,
		InputTokens: op.inputTokens, ResultTokens: snapshot.ProjectedTokens,
		Messages: op.messages, Summary: op.summary, Archive: op.archive,
	}
	if summary != "" {
		info.Summary = summary
	}
	if snapshot.LastReceipt != nil && info.Applied {
		info.Messages, info.Archive = snapshot.LastReceipt.CoveredCount, snapshot.LastReceipt.Archive
	}
	terminal := maintenanceTerminalStatus(status)
	if !terminal {
		c.applyMaintenanceInfoLocked(op, info)
	}
	c.mu.Unlock()
	err := event.EmitChecked(c.sink, event.Event{Kind: event.SessionOperation, SessionOperation: info})
	if err == nil && terminal {
		c.mu.Lock()
		c.applyMaintenanceInfoLocked(op, info)
		op.terminal = true
		c.mu.Unlock()
	}
	return err
}

func maintenanceRuntimePhase(activity string) session.RuntimePhase {
	switch activity {
	case "cancelling":
		return session.RuntimeCancelling
	case "finalizing":
		return session.RuntimeFinalizing
	case "recovery_required":
		return session.RuntimeRecoveryRequired
	default:
		return session.RuntimeRunning
	}
}

func (c *Controller) applyMaintenanceInfoLocked(op *controllerMaintenance, info *event.SessionOperationInfo) {
	op.activity, op.status, op.revision = info.Activity, info.Status, info.OperationRevision
	op.errorCode, op.detail, op.applied = info.ErrorCode, info.Detail, info.Applied
	op.resultTokens, op.messages, op.summary, op.archive = info.ResultTokens, info.Messages, info.Summary, info.Archive
	c.noteExecutionLocked(maintenanceRuntimePhase(op.activity), session.MaintenanceActivity)
}

func maintenanceTransitionAllowed(op *controllerMaintenance, status, code string) bool {
	if op.terminal {
		return false
	}
	if status == "running" {
		return op.revision == 0
	}
	if status == "cancelling" {
		return op.activity == "running"
	}
	if status == "recovery_required" {
		return code != "cancel_timeout" || op.cancelSignalled && (op.activity == "running" || op.activity == "cancelling")
	}
	// Only the settled worker may leave timeout recovery; persistence recovery
	// is retained until its owner is explicitly recovered or closed.
	if op.activity == "recovery_required" && op.errorCode != "cancel_timeout" {
		return false
	}
	return status == "finalizing" || maintenanceTerminalStatus(status)
}

func (c *Controller) signalMaintenanceCancel() (id string, present, cancelled bool) {
	c.mu.Lock()
	op := c.maintenance
	if op == nil {
		c.mu.Unlock()
		return "", false, false
	}
	id = op.id
	if op.activity == "finalizing" || op.activity == "recovery_required" || op.terminal {
		c.mu.Unlock()
		return id, true, false
	}
	if op.cancelSignalled {
		c.mu.Unlock()
		return id, true, false
	}
	op.cancelSignalled = true
	cancel := op.cancel
	applied := op.applied
	c.mu.Unlock()
	// Signal first. Event delivery and every queue/disk operation happen after
	// the provider/adapter has observed cancellation.
	cancel()
	c.recordLifecycle("cancel_signalled", "maintenance", id, 0, "")
	c.startMaintenanceCancellationWatchdog(op)
	// Cancellation receipts must not wait for the event lane or a runtime
	// sampler. Publication revalidates this exact operation and its phase.
	go func() {
		_ = c.emitMaintenanceOperation(op, "cancelling", "", "", c.ContextMaintenanceSnapshot(), applied)
		c.refreshRuntimeState(event.Event{})
	}()
	return id, true, true
}

func (c *Controller) startMaintenanceCancellationWatchdog(op *controllerMaintenance) {
	if c == nil || op == nil {
		return
	}
	go func() {
		timer := time.NewTimer(c.cancellationGrace())
		defer timer.Stop()
		select {
		case <-op.done:
			return
		case <-timer.C:
		}
		c.mu.Lock()
		if c.maintenance != op || !op.cancelSignalled || op.activity != "running" && op.activity != "cancelling" {
			c.mu.Unlock()
			return
		}
		applied := op.applied
		c.mu.Unlock()
		_ = c.emitMaintenanceOperation(op, "recovery_required", "cancel_timeout", "the compaction worker did not stop within the cancellation grace period", c.ContextMaintenanceSnapshot(), applied)
		c.refreshRuntimeState(event.Event{})
	}()
}

func (c *Controller) maintenanceSnapshotLocked() *event.MaintenanceState {
	if c.maintenance == nil {
		return nil
	}
	op := c.maintenance
	return &event.MaintenanceState{
		OperationID: op.id, OperationRevision: op.revision, RuntimeEpoch: op.runtimeEpoch,
		Kind: op.kind, Activity: op.activity, Status: op.status,
		ErrorCode: op.errorCode, Detail: op.detail, Applied: op.applied,
		InputTokens: op.inputTokens, ResultTokens: op.resultTokens, Messages: op.messages,
	}
}

func maintenanceTerminalStatus(status string) bool {
	switch status {
	case "completed", "noop", "cancelled", "partially_completed", "failed", "interrupted":
		return true
	default:
		return false
	}
}

// finishMaintenanceOperation releases the maintenance owner and reserves the
// oldest volatile follow-up under the same lock. This closes the idle gap in
// which a new submission could otherwise jump ahead of accepted guidance.
func (c *Controller) finishMaintenanceOperation(op *controllerMaintenance) {
	var next queuedTurn
	var runCtx context.Context
	var cancel context.CancelFunc
	var started bool
	c.mu.Lock()
	if c.maintenance != op {
		c.mu.Unlock()
		return
	}
	if runtime := c.turns.runtime; runtime != nil && !runtime.FinishMaintenanceExecution(c.turns.generation) {
		c.mu.Unlock()
		_ = c.retainMaintenanceRecovery(op, "execution_owner_lost", ErrMaintenanceRecovery, c.ContextMaintenanceSnapshot(), op.applied)
		return
	}
	c.maintenance = nil
	close(op.done)
	closed := c.closed
	if !closed && !c.recoveryRequiredLocked() && c.authentication.admissionError() == nil {
		if candidate, ok := c.popNextPendingLocked(); ok {
			next = candidate
			runCtx, cancel, started = c.startTurnLocked(context.Background(), next)
			if !started {
				c.turns.pending = append([]queuedTurn{next}, c.turns.pending...)
				c.turns.wake = true
				c.enterRecoveryLocked("execution_owner_lost")
			}
		}
	}
	if !started {
		c.noteExecutionLocked(session.RuntimeIdle, "")
	}
	c.mu.Unlock()
	op.cancel()
	c.refreshRuntimeState(event.Event{})
	if closed {
		c.finalizeControllerClose()
		return
	}
	if started {
		if next.onStart != nil {
			next.onStart()
		}
		c.spawnGuardedTurn(runCtx, cancel, next)
		return
	}
	c.mu.Lock()
	recovery := c.recoveryRequiredLocked()
	c.mu.Unlock()
	if !recovery && c.authentication.admissionError() == nil {
		c.maybeDispatchInbox()
		c.kickGoalDriver()
	}
}

// ActiveMaintenanceOperationID lets result-capable hosts correlate a
// management-command receipt with the authoritative operation event. Empty is
// valid when the maintenance task reached a terminal state before the caller
// received its receipt.
func (c *Controller) ActiveMaintenanceOperationID() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maintenance == nil {
		return ""
	}
	return c.maintenance.id
}
