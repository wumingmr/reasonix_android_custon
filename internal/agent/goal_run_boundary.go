package agent

import (
	"context"
	"errors"
	"fmt"

	"reasonix/internal/provider"
)

// maxStepsPause is a resumable stop after a positive model-round budget.
type maxStepsPause struct {
	steps int
	key   string
}

func (e *maxStepsPause) Error() string {
	return fmt.Sprintf("paused after %d tool-call rounds (%s) — the work so far is saved; send another message to continue, or set %s higher or to 0 for no limit", e.steps, e.key, e.key)
}

func isToolLoopPause(err error) bool {
	var maxPause *maxStepsPause
	var budgetPause *taskBudgetPause
	return errors.As(err, &maxPause) || errors.As(err, &budgetPause)
}

// HostProgressSignatures exposes successful evidence identities to the Goal FSM.
func (a *Agent) HostProgressSignatures() []string {
	if a == nil || a.task.ledger == nil {
		return nil
	}
	return a.task.ledger.SuccessfulProgressSignaturesSince(0)
}

// resetTurnEvidence starts a fresh execution-fact projection for a new task
// scope. Facts remain useful for display and Goal accounting but do not gate
// later tools or model completion.
func (a *Agent) resetTurnEvidence() {
	a.task.restartLedger()
}

func (a *Agent) stopUnexecutedBoundaryCalls(ctx context.Context, state *turnRuntime, calls []provider.ToolCall, usage *provider.Usage) (error, bool) {
	switch {
	case state.graceRound && !a.allowsBoundaryTurnFinalizer(ctx, state, calls):
		if err := a.pairUnexecutedGraceCalls(ctx, calls, "blocked: the tool-call round budget is exhausted; no more tools will run in this turn"); err != nil {
			return err, true
		}
		return a.gracePause(state), true
	default:
		return nil, false
	}
}
