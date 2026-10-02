package control

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/session"
	"reasonix/internal/turnevent"
)

// RuntimeStateReader is optional so older embedders of SessionAPI keep working.
type RuntimeStateReader interface {
	RuntimeStateSnapshot() event.RuntimeStateSnapshot
}

type controllerRuntimeState struct {
	mu             sync.Mutex // serializes sampling, commit and publication order; never held by observers
	published      atomic.Pointer[event.RuntimeStateSnapshot]
	snapshot       event.RuntimeStateSnapshot
	ledger         *turnevent.Ledger
	path           string
	activity       string
	sink           event.Sink
	pending        *event.RuntimeStateSnapshot
	draining       bool
	jobUnsubscribe func()
}

func newRuntimeStateEpoch() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(bytes[:])
}

// RuntimeStateSnapshot first commits a fresh projection from the current owners
// and then returns that immutable boundary. This closes the small callback lag
// after a background job starts or exits without making readers combine fields
// from separate snapshots.
func (c *Controller) RuntimeStateSnapshot() event.RuntimeStateSnapshot {
	if c == nil {
		return event.RuntimeStateSnapshot{Todos: []event.Todo{}, Interactions: []event.PendingInteraction{}}
	}
	c.refreshRuntimeState(event.Event{})
	c.runtimeState.mu.Lock()
	defer c.runtimeState.mu.Unlock()
	return cloneRuntimeState(c.runtimeState.snapshot)
}

func cloneRuntimeState(in event.RuntimeStateSnapshot) event.RuntimeStateSnapshot {
	out := in
	out.Todos = append([]event.Todo{}, in.Todos...)
	out.Interactions = append([]event.PendingInteraction{}, in.Interactions...)
	if in.Recovery != nil {
		recovery := *in.Recovery
		out.Recovery = &recovery
	}
	if in.Maintenance != nil {
		maintenance := *in.Maintenance
		out.Maintenance = &maintenance
	}
	if in.Goal != nil {
		goal := *in.Goal
		if in.Goal.MaxGoalRounds != nil {
			limit := *in.Goal.MaxGoalRounds
			goal.MaxGoalRounds = &limit
		}
		if in.Goal.BlockedReason != nil {
			reason := *in.Goal.BlockedReason
			goal.BlockedReason = &reason
		}
		out.Goal = &goal
	}
	return out
}

func (c *Controller) initializeRuntimeState() {
	c.runtimeState.mu.Lock()
	c.runtimeState.sink = c.sink
	c.runtimeState.mu.Unlock()
	c.refreshRuntimeState(event.Event{})
	if c.jobs != nil {
		// A manager may be shared across a controller rebuild. Subscribe to all
		// session transitions and filter against the current committed binding.
		_, stop := c.jobs.SubscribeRuntime("", func(state jobs.RuntimeState) {
			if c.receivesBackgroundRuntimeEvents() {
				c.refreshRuntimeState(event.Event{})
			}
		})
		c.runtimeState.mu.Lock()
		c.runtimeState.jobUnsubscribe = stop
		c.runtimeState.mu.Unlock()
		c.refreshRuntimeState(event.Event{})
	}
}

// refreshRuntimeState is a commit boundary, not a read-side workaround. Every
// lifecycle and job boundary calls it after releasing its owning locks. A
// single sampler re-reads current owners instead of replaying stale booleans.
func (c *Controller) refreshRuntimeState(e event.Event) {
	c.refreshRuntimeStateAttempt(e, 0)
}

// refreshRuntimeStateAttempt retries an unstable multi-owner sample at most
// three times. Lifecycle callbacks will sample again after later transitions;
// a perpetually changing runtime must not grow the stack or monopolize a caller.
func (c *Controller) refreshRuntimeStateAttempt(e event.Event, attempt int) {
	if c == nil {
		return
	}
	r := &c.runtimeState
	r.mu.Lock()
	if r.sink == nil {
		r.mu.Unlock()
		return
	} // construction has not finished
	c.mu.Lock()
	running, finishing, closed, cancelling, path := c.bodyActiveLocked(), c.finalizingLocked(), c.closed, c.cancelRequestedLocked(), c.sessionPath
	maintenance := c.maintenanceSnapshotLocked()
	c.mu.Unlock()
	_, v3Runtime, exclusiveSession := c.v3Binding()
	var v3RuntimeSnapshot session.RuntimeSnapshot
	if exclusiveSession && v3Runtime != nil {
		v3RuntimeSnapshot = v3Runtime.StateSnapshot()
	}
	ledger := c.turnEventLedger()
	initialized := r.snapshot.SchemaVersion == 1
	base, activity := r.snapshot, r.activity
	if r.snapshot.ProjectionEpoch == "" || r.path != path || r.ledger != ledger {
		base = event.RuntimeStateSnapshot{ProjectionEpoch: newRuntimeStateEpoch(), RuntimeEpoch: newRuntimeStateEpoch()}
		activity = ""
	}
	next := base
	next.SchemaVersion = 1
	goalView, goalErr := c.goalLifecycleView()
	next.Goal = goalView
	next.GoalError = ""
	if goalErr != nil {
		next.GoalError = goalErr.Error()
	}
	if ref, ok := c.SessionRef(); ok {
		next.HostID = ref.HostID
		next.SessionID = ref.SessionID
		next.SessionCodec = session.Codec
		next.RuntimeEpoch = v3RuntimeSnapshot.Epoch
		next.ActivityRevision = v3RuntimeSnapshot.ActivityRevision
	} else {
		next.HostID = ""
		next.SessionID = ""
		next.SessionCodec = ""
	}
	if ledger != nil {
		next.TurnID, next.TurnStatus, next.TurnEventSeq = ledger.RuntimeIdentity()
	}
	v3Snapshot, hasV3Snapshot := c.sessionStateSnapshot()
	applyRuntimeSessionState(&next, v3Snapshot, hasV3Snapshot)
	next.HeadID = agent.BranchID(path)
	if exclusiveSession {
		next.HeadID = ""
	}
	if hasV3Snapshot {
		// The typed v3 projection above is authoritative.
	} else if ledger != nil {
		next.Todos, next.TodoWritten = ledger.TodoState()
	} else {
		next.Todos, next.TodoWritten = c.volatileTodoState()
	}
	// Keep the empty wire shape stable. Ledger projections intentionally use a
	// nil backing slice internally, while every public snapshot promises [].
	// Normalizing before the semantic comparison prevents a read from creating
	// a new revision solely because nil and an empty slice differ to reflect.
	if next.Todos == nil {
		next.Todos = []event.Todo{}
	}
	setRuntimePhase(&next, exclusiveSession, v3Runtime, v3RuntimeSnapshot, running, finishing, closed, cancelling)
	applyMaintenanceRuntimeState(&next, maintenance, running, finishing, closed, cancelling)
	identities, promptRevision := c.promptOwner.IdentitiesRevision()
	next.PendingPrompt = len(identities) > 0
	next.Interactions = make([]event.PendingInteraction, len(identities))
	for i, identity := range identities {
		next.Interactions[i] = event.PendingInteraction{RequestID: identity.PromptID, ToolCallID: identity.ToolCallID, Kind: string(identity.Kind), HeadID: next.HeadID, TurnID: identity.TurnID, RuntimeEpoch: identity.RuntimeEpoch}
	}
	// Compatibility consumers may still use Cancellable as a button-state
	// hint. Derive it only from the authoritative phase/request projection so a
	// worker crossing into the finishing window cannot make an accepted cancel
	// briefly look unavailable.
	next.Cancellable = next.Phase == "executing" || next.Phase == "cancelling" || next.PendingPrompt
	if maintenance != nil && (maintenance.Activity == "finalizing" || maintenance.Activity == "recovery_required") {
		next.Cancellable = false
	}
	next.BackgroundJobs = 0
	if c.jobs != nil {
		next.BackgroundJobs = len(c.jobs.RunningForSession(c.parentSessionID()))
	}
	// Sampling owners is off their locks. Do not commit a mixture if the
	// admission/close/binding boundary advanced while another owner was read.
	stable := c.runtimeBoundaryStable(running, finishing, closed, cancelling, path, maintenance)
	currentGoal, currentGoalErr := c.goalLifecycleView()
	stable = stable && reflect.DeepEqual(goalView, currentGoal)
	stable = stable && ((goalErr == nil && currentGoalErr == nil) || (goalErr != nil && currentGoalErr != nil && goalErr.Error() == currentGoalErr.Error()))
	if exclusiveSession && v3Runtime != nil {
		_, currentRuntime, currentExclusive := c.v3Binding()
		stable = stable && currentExclusive && currentRuntime == v3Runtime && currentRuntime.StateSnapshot().ActivityRevision == v3RuntimeSnapshot.ActivityRevision
	}
	if !stable || ledger != c.turnEventLedger() || promptRevision != c.promptOwner.Revision() {
		r.mu.Unlock()
		if attempt < 2 {
			c.refreshRuntimeStateAttempt(event.Event{}, attempt+1)
		}
		return
	}
	if closed && !running && next.BackgroundJobs == 0 && r.jobUnsubscribe != nil {
		stop := r.jobUnsubscribe
		r.jobUnsubscribe = nil
		defer stop()
	}
	activity = runtimeActivity(next, e, activity)
	next.Activity = activity
	setRuntimeRecovery(&next, v3Snapshot, hasV3Snapshot, ledger, activity)
	// Token deltas do not need runtime notifications. Keep the last published
	// watermark until a semantic state changes, avoiding a second token stream.
	compare := next
	compare.TurnEventSeq = r.snapshot.TurnEventSeq
	if reflect.DeepEqual(compare, r.snapshot) {
		r.mu.Unlock()
		return
	}
	next.Revision++
	r.commitSnapshot(next)
	r.path, r.ledger, r.activity = path, ledger, activity
	defer slog.Debug("runtime state committed", "source", "controller", "epoch", next.RuntimeEpoch[:8], "revision", next.Revision, "phase", next.Phase)
	if !initialized {
		r.mu.Unlock()
		return
	}
	pending := cloneRuntimeState(next)
	r.pending = &pending
	if r.draining {
		r.mu.Unlock()
		return
	}
	r.draining = true
	r.mu.Unlock()
	go c.publishRuntimeState()
}

func (c *Controller) publishRuntimeState() {
	r := &c.runtimeState
	for {
		r.mu.Lock()
		if r.pending == nil {
			r.draining = false
			r.mu.Unlock()
			return
		}
		snapshot, sink := cloneRuntimeState(*r.pending), r.sink
		r.pending = nil
		r.mu.Unlock()
		event.PublishRuntimeState(sink, snapshot)
	}
}

func runtimeActivity(state event.RuntimeStateSnapshot, e event.Event, activity string) string {
	if state.Maintenance != nil {
		switch state.Maintenance.Activity {
		case "cancelling":
			return "stopping_compaction"
		case "finalizing":
			return "saving_compaction"
		case "recovery_required":
			return "maintenance_recovery_required"
		default:
			return "compacting"
		}
	}
	if state.PendingPrompt {
		return "waiting_input"
	}
	if state.Phase == "cancelling" {
		return "cancelling"
	}
	if state.Phase == "recovery_required" {
		return "recovery_required"
	}
	if state.Phase == "executing" {
		if e.TurnID == "" || e.TurnID == state.TurnID {
			switch e.Kind {
			case event.Text, event.Message:
				activity = "streaming"
			case event.TurnStarted, event.Reasoning, event.ToolDispatch, event.ToolProgress, event.ToolResult, event.CompactionStarted, event.Retrying:
				activity = "thinking"
			}
		}
		if activity == "" {
			activity = "thinking"
		}
	} else {
		activity = ""
	}
	return activity
}

func setRuntimePhase(next *event.RuntimeStateSnapshot, exclusiveSession bool, v3Runtime *session.Runtime, v3RuntimeSnapshot session.RuntimeSnapshot, running, finishing, closed, cancelling bool) {
	next.Phase = "idle"
	if closed {
		next.Phase = "closed"
		return
	}
	if exclusiveSession && v3Runtime != nil {
		switch v3RuntimeSnapshot.Phase {
		case session.RuntimeRunning:
			next.Phase = "executing"
		case session.RuntimeCancelling:
			next.Phase = "cancelling"
		case session.RuntimeFinalizing:
			next.Phase = "finishing"
		case session.RuntimeRecoveryRequired:
			next.Phase = "recovery_required"
		case session.RuntimeClosed:
			next.Phase = "closed"
		}
	} else {
		switch {
		case next.TurnStatus == event.TurnRecoveryRequired:
			next.Phase = "recovery_required"
		case cancelling:
			next.Phase = "cancelling"
		case running:
			next.Phase = "executing"
		case finishing:
			next.Phase = "finishing"
		case closed:
			next.Phase = "closed"
		}
	}
}

func applyRuntimeSessionState(next *event.RuntimeStateSnapshot, v3Snapshot session.Snapshot, hasV3Snapshot bool) {
	if hasV3Snapshot {
		snapshot := v3Snapshot
		next.CommittedSeq = snapshot.EventSequence
		next.DurableSeq = snapshot.DurableSequence
		next.Persistence = string(snapshot.PersistenceStatus)
		next.PersistenceErr = snapshot.PersistenceError
		next.Todos = append([]event.Todo(nil), snapshot.Projection.Todos...)
		next.TodoWritten = snapshot.Projection.TodoWritten
		if snapshot.Projection.TurnID != "" {
			next.TurnID = snapshot.Projection.TurnID
			next.TurnStatus = snapshot.Projection.TurnStatus
		}
		if snapshot.Projection.Recovery != nil && snapshot.Projection.Recovery.State == "recovery_required" {
			next.TurnStatus = event.TurnRecoveryRequired
		}
	} else {
		next.CommittedSeq = next.TurnEventSeq
		next.DurableSeq = next.TurnEventSeq
		next.Persistence = "unavailable"
	}
}

func setRuntimeRecovery(next *event.RuntimeStateSnapshot, v3Snapshot session.Snapshot, hasV3Snapshot bool, ledger *turnevent.Ledger, activity string) {
	next.Recovery = nil
	if next.Phase == "recovery_required" {
		if hasV3Snapshot && v3Snapshot.Projection.Recovery != nil {
			recovery := *v3Snapshot.Projection.Recovery
			next.Recovery = &recovery
		} else if ledger != nil {
			next.Recovery = ledger.RecoveryStatus()
		}
		if next.Recovery == nil {
			next.Recovery = &event.RecoveryStatus{State: "recovery_required", Phase: activity, Reason: "runtime state requires recovery"}
		} else if next.Recovery.Phase == "" {
			next.Recovery.Phase = activity
		}
	}
}

func (c *Controller) runtimeBoundaryStable(running, finishing, closed, cancelling bool, path string, maintenance *event.MaintenanceState) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return running == c.bodyActiveLocked() && finishing == c.finalizingLocked() && closed == c.closed && cancelling == c.cancelRequestedLocked() && path == c.sessionPath && reflect.DeepEqual(maintenance, c.maintenanceSnapshotLocked())
}

func applyMaintenanceRuntimeState(next *event.RuntimeStateSnapshot, maintenance *event.MaintenanceState, running, finishing, closed, cancelling bool) {
	next.Maintenance = maintenance
	if maintenance != nil && !closed {
		switch maintenance.Activity {
		case "cancelling":
			next.Phase = "cancelling"
		case "finalizing":
			next.Phase = "finishing"
		case "recovery_required":
			next.Phase = "recovery_required"
		default:
			next.Phase = "executing"
		}
	}
	// Close is immediately authoritative for the public controller view even
	// while the session runtime remains in its private finalizing barrier. The
	// latter keeps commit authority alive until TurnDone is durable; exposing it
	// here would make a closed controller look runnable again.
	next.Running = (running || finishing || maintenance != nil) && !closed
	next.CancelRequested = (cancelling || maintenance != nil && maintenance.Activity == "cancelling") && !closed
}
