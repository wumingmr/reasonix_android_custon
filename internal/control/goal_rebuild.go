package control

import (
	goaldomain "reasonix/internal/goal"
	"reasonix/internal/session"
)

// InheritLifecycleFrom carries same-session lifecycle state across controller
// rebuilds, such as model switches that preserve the conversation.
func (c *Controller) InheritLifecycleFrom(prev *Controller) error {
	if prev == nil {
		return nil
	}
	if c.workspaceRoot == prev.workspaceRoot && c.executor != nil {
		c.executor.InheritFileObservationsFrom(prev.executor)
	}
	prev.mu.Lock()
	started := prev.startedOnce
	turn := prev.turn
	prev.mu.Unlock()

	c.mu.Lock()
	c.startedOnce = started
	if c.turn < turn {
		c.turn = turn
	}
	c.mu.Unlock()

	_, currentRuntime, currentExclusive := c.v3Binding()
	_, previousRuntime, previousExclusive := prev.v3Binding()
	if !currentExclusive || !previousExclusive || currentRuntime == nil || currentRuntime != previousRuntime {
		return nil
	}
	prev.goalDriverMu.Lock()
	defer prev.goalDriverMu.Unlock()
	if prev.goalDriverPending || prev.goalDriverActive != nil {
		return session.ErrRuntimeBusy
	}
	prev.goalLifecycleMu.RLock()
	previousMachine, previousLoadErr := prev.goalLifecycle, prev.goalLifecycleLoadErr
	prev.goalLifecycleMu.RUnlock()
	if previousLoadErr != nil {
		return previousLoadErr
	}
	c.goalLifecycleMu.Lock()
	if c.goalLifecycleLoadErr != nil {
		err := c.goalLifecycleLoadErr
		c.goalLifecycleMu.Unlock()
		return err
	}
	candidate := c.goalLifecycle.Clone()
	if err := candidate.InheritRuntimeFrom(previousMachine); err != nil {
		c.goalLifecycleMu.Unlock()
		return err
	}
	c.goalLifecycle = candidate
	c.goalLifecycleMu.Unlock()

	prev.goalResourceMu.Lock()
	tokensUsed, requestsUsed := prev.goalTokensUsed, prev.goalRequestsUsed
	tokenLimit, extensions := prev.goalTokenLimit, prev.goalBudgetExtensions
	previousBudget := prev.goalTokenBudget
	prev.goalResourceMu.Unlock()
	c.goalResourceMu.Lock()
	c.goalTokensUsed = tokensUsed
	c.goalRequestsUsed = requestsUsed
	c.goalBudgetExtensions = extensions
	if c.goalTokenBudget == previousBudget {
		c.goalTokenLimit = tokenLimit
	} else if c.goalTokenBudget <= 0 {
		c.goalTokenLimit = 0
	} else {
		c.goalTokenLimit = c.goalTokenBudget * (extensions + 1)
	}
	c.goalResourceMu.Unlock()
	if view := candidate.Get(); view != nil && view.Phase == goaldomain.PhaseActive && view.Activation == goaldomain.ActivationArmed {
		c.goalDriverControl.inherited.Store(true)
	}
	return nil
}

// ActivateGoalDriverAfterRebuild is called only after a host has published the
// replacement controller. It keeps an unpublished build from racing the old
// controller for the shared SessionRuntime.
func (c *Controller) ActivateGoalDriverAfterRebuild() {
	if c != nil && c.goalDriverControl.inherited.Swap(false) {
		c.kickGoalDriver()
	}
}
