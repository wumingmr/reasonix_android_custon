package session

import (
	"context"
)

// ExecutionControl is the session-scoped turn-loop bound to a Runtime.
// The loop is the execution authority; Runtime only forwards Cancel and
// caches the last NoteExecution from the bound generation.
type ExecutionControl interface {
	Snapshot() RuntimeSnapshot
	Cancel() bool
}

const MaintenanceActivity = "maintenance"

// FinishMaintenanceExecution releases a settled maintenance worker into the
// finalizing handoff barrier. Only its exact owner may clear a cancellation
// timeout; durable business recovery and failed persistence remain fenced.
func (r *Runtime) FinishMaintenanceExecution(generation uint64) bool {
	if r == nil || generation == 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	owner := r.execution.Load()
	if owner == nil || owner.generation != generation || r.activity != MaintenanceActivity || r.phase == RuntimeClosed {
		return false
	}
	state := r.session.StateSnapshot()
	if state.PersistenceStatus == PersistenceFailed || state.PersistenceStatus == PersistenceUncertain ||
		state.Projection.Recovery != nil && state.Projection.Recovery.State == "recovery_required" {
		return false
	}
	r.phase = RuntimeFinalizing
	r.canceling.Store(false)
	r.revision.Add(1)
	return true
}

type executionBinding struct {
	generation uint64
	control    ExecutionControl
}

// BindExecution installs the first generation-scoped turn-loop. Replacing an
// existing owner is always explicit through ReplaceExecution so constructing a
// candidate controller cannot steal Stop or mutation authority.
func (r *Runtime) BindExecution(control ExecutionControl) uint64 {
	if r == nil || control == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase == RuntimeClosed || r.phase == RuntimeRecoveryRequired || r.execution.Load() != nil {
		return 0
	}
	gen := r.bindGen.Add(1)
	r.execution.Store(&executionBinding{generation: gen, control: control})
	r.revision.Add(1)
	return gen
}

// ReplaceExecution transfers an idle runtime from the exact expected
// generation to a replacement turn-loop. The lock acquisition is the
// linearization point: generations are allocated only after ownership and
// phase validation, so they can never be published out of order.
func (r *Runtime) ReplaceExecution(expectedGeneration uint64, control ExecutionControl) uint64 {
	if r == nil || expectedGeneration == 0 || control == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.execution.Load()
	if cur == nil || cur.generation != expectedGeneration || r.phase != RuntimeIdle {
		return 0
	}
	gen := r.bindGen.Add(1)
	r.execution.Store(&executionBinding{generation: gen, control: control})
	r.revision.Add(1)
	return gen
}

// ReplaceExecutionAndCommit atomically accepts a candidate's prevalidated
// session mutation and transfers an idle Runtime to that candidate. A failed
// commit leaves the outgoing execution owner untouched.
func (r *Runtime) ReplaceExecutionAndCommit(expectedGeneration uint64, control ExecutionControl, prepared PreparedBatch) (uint64, Commit, error) {
	if r == nil || expectedGeneration == 0 || control == nil {
		prepared.Release()
		return 0, Commit{}, ErrStaleExecution
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.execution.Load()
	if cur == nil || cur.generation != expectedGeneration || r.phase != RuntimeIdle {
		prepared.Release()
		return 0, Commit{}, ErrStaleExecution
	}
	commit, err := r.session.CommitPrepared(prepared)
	if err != nil {
		return 0, Commit{}, err
	}
	gen := r.bindGen.Add(1)
	r.execution.Store(&executionBinding{generation: gen, control: control})
	r.revision.Add(1)
	return gen, commit, nil
}

// OwnsExecution reports whether generation is the currently bound turn-loop.
// It is a snapshot only; mutations that require fencing must validate again
// while holding r.mu.
func (r *Runtime) OwnsExecution(generation uint64) bool {
	if r == nil || generation == 0 {
		return false
	}
	cur := r.execution.Load()
	return cur != nil && cur.generation == generation
}

// BeginExecution atomically admits a turn for the exact execution owner. A
// queued turn may move directly from finalizing to running; no observer sees a
// false idle boundary between turns.
func (r *Runtime) BeginExecution(generation uint64, activity string) bool {
	if r == nil || generation == 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.execution.Load()
	if cur == nil || cur.generation != generation {
		return false
	}
	if r.phase != RuntimeIdle && r.phase != RuntimeFinalizing {
		return false
	}
	r.phase = RuntimeRunning
	r.activity = activity
	r.canceling.Store(false)
	r.revision.Add(1)
	return true
}

// CommitPreparedForExecution accepts a prepared batch only while generation
// is the exact execution owner. Preparation remains outside the Runtime lock;
// the short ownership check and in-memory Session acceptance form one commit
// boundary so a controller cutover cannot race a stale writer into the log.
func (r *Runtime) CommitPreparedForExecution(generation uint64, prepared PreparedBatch) (Commit, error) {
	if r == nil || generation == 0 {
		prepared.Release()
		return Commit{}, ErrStaleExecution
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.execution.Load()
	if cur == nil || cur.generation != generation {
		prepared.Release()
		return Commit{}, ErrStaleExecution
	}
	return r.session.CommitPrepared(prepared)
}

// CommitPreparedForTurn makes the owner/turn check and terminal acceptance one
// critical section. A prepared completion cannot close a successor's turn.
func (r *Runtime) CommitPreparedForTurn(generation uint64, turnID string, prepared PreparedBatch) (Commit, error) {
	if r == nil || generation == 0 {
		prepared.Release()
		return Commit{}, ErrStaleExecution
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.execution.Load()
	if cur == nil || cur.generation != generation || r.session.StateSnapshot().Projection.TurnID != turnID {
		prepared.Release()
		return Commit{}, ErrStaleExecution
	}
	return r.session.CommitPrepared(prepared)
}

// UnbindExecution releases only the exact generation. A superseded controller
// cannot clear the replacement's control binding.
func (r *Runtime) UnbindExecution(generation uint64) {
	if r == nil || generation == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.execution.Load()
	if cur == nil || cur.generation != generation || (r.phase.busy() && r.phase != RuntimeRecoveryRequired) {
		return
	}
	r.execution.Store(nil)
	r.revision.Add(1)
}

// NoteExecution records a phase transition from the exact bound generation.
// Validation and mutation share r.mu so a replacement cannot land between
// them and let an older controller alter the new owner's phase.
func (r *Runtime) NoteExecution(generation uint64, phase RuntimePhase, activity string) {
	if r == nil || generation == 0 {
		return
	}
	r.mu.Lock()
	cur := r.execution.Load()
	if cur == nil || cur.generation != generation {
		r.mu.Unlock()
		return
	}
	if r.phase == RuntimeClosed {
		r.mu.Unlock()
		return
	}
	if r.phase == RuntimeRecoveryRequired && phase != RuntimeRecoveryRequired && phase != RuntimeClosed {
		r.mu.Unlock()
		return
	}
	if r.phase == phase && r.activity == activity {
		r.mu.Unlock()
		return
	}
	r.phase = phase
	r.activity = activity
	r.revision.Add(1)
	if phase == RuntimeCancelling || phase == RuntimeRecoveryRequired {
		r.canceling.Store(true)
	} else {
		r.canceling.Store(false)
	}
	owner := r.owner
	r.mu.Unlock()
	if phase == RuntimeIdle && owner != nil {
		_ = owner.closeIfUnbound(context.Background(), r)
	}
}

func (r *Runtime) loadExecution() *executionBinding {
	if r == nil {
		return nil
	}
	return r.execution.Load()
}

// Busy reports whether a phase still owns live execution or finalization
// work. Hosts use this shared definition so finalizing sessions cannot vanish
// from running lists before their terminal commit completes.
func (p RuntimePhase) Busy() bool {
	switch p {
	case RuntimeRunning, RuntimeCancelling, RuntimeFinalizing, RuntimeRecoveryRequired:
		return true
	default:
		return false
	}
}

func (p RuntimePhase) busy() bool { return p.Busy() }

func (r *Runtime) executionBusy() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	phase := r.phase
	bound := r.execution.Load() != nil
	r.mu.Unlock()
	return phase.busy() && bound
}
