package agent

import (
	"reasonix/internal/completion"
	"reasonix/internal/runtimepolicy"
)

// turnRuntime is the host state for exactly one Agent.Run. beginRunTurn builds
// it in a single assignment, so a field added here starts the next turn zeroed.
// State an external caller arms before a Run lives in pendingTurn; state that
// outlives the Run lives in taskRuntime or sessionRuntime.
type turnRuntime struct {
	runMaxSteps    int
	runMaxStepsKey string

	terminal    terminalProtocolState
	usedAnyTool bool
	graceRound  bool

	input          string
	workDurationMs func() int64

	// budget is the turn's spend axis: tokens, money, wall clock.
	budget runBudget
	// landCause records why the grace round was armed, so the pause the Run
	// ends with names the axis that actually stopped it.
	landCause landCause

	// turnInput is the owning task text for explicit constraints and recovery.
	turnInput string
	// completion is the report built as the turn ends; the host reads it while
	// emitting TurnDone, before the next turn resets this state.
	completion          *completion.Report
	deliveryScopeActive bool
	// recoveryTaskSummary is the bounded task text for this Agent.Run. It lets
	// a shared recovery gate review sub-agent mutations against the child
	// task, rather than the root controller transcript.
	recoveryTaskSummary string

	repeatKey   string
	repeatCount int
	loop        turnLoopState

	// constraints and engine are frozen at the start of the Run.
	constraints runtimepolicy.Constraints
	engine      *runtimepolicy.Engine

	// reviewWarnings are warn-level findings to surface in the final summary.
	reviewWarnings []string

	// lastReasoning is the previous executor round's reasoning-token spend,
	// read by the governor trigger (live policy and fork capture alike).
	lastReasoning int

	phase phaseClock

	// sessionContext is the content-free diagnostic for the snapshot selected
	// before this real user turn. It is attached to Usage events only.
	sessionContext turnContextDiagnostics
}

// terminalProtocolState groups the run's terminal-protocol bookkeeping: the
type terminalProtocolState struct {
	// emptyFinalBlocks counts consecutive reasoning-only stops retried for a
	// visible final answer.
	emptyFinalBlocks int
}

// pendingTurn is what someone outside the Run arms for the next one: a
// sub-agent spawner, the turn that just failed readiness, or the fork capture.
// It is deliberately not in turnRuntime — beginRunTurn builds that fresh, and
// state armed before it exists would be wiped by the same assignment that makes
// turnRuntime safe.
type pendingTurn struct {
	// forkRestore, when armed, swaps the frozen fork-bundle conversation in
	// right after beginRunTurn — the counterfactual-continuation seam.
	forkRestore func(*turnRuntime)
}
