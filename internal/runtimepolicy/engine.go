package runtimepolicy

import "sync"

// Engine decides whether an individual action is allowed. It owns no task
// acceptance state and does not judge when a model should finish.
type Engine struct {
	mu          sync.Mutex
	guards      []Guard
	constraints Constraints
}

func NewEngine(constraints Constraints, extra ...Guard) *Engine {
	guards := []Guard{PlanGuard{}, ConstraintGuard{Constraints: constraints}, MutationDependencyGuard{}, OpaqueWriterGuard{}}
	return &Engine{guards: append(guards, extra...), constraints: constraints}
}

func (e *Engine) Constraints() Constraints {
	if e == nil {
		return Constraints{}
	}
	return e.constraints
}

// BeforeTool returns a decision before any permission prompt or execution.
// Serializing custom guards preserves their existing concurrency contract.
func (e *Engine) BeforeTool(ctx CallContext) GuardDecision {
	if e == nil {
		return GuardDecision{Action: GuardAbstain}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var decisions []GuardDecision
	for _, guard := range e.guards {
		decisions = append(decisions, guard.BeforeTool(ctx))
	}
	return MergeDecisions(decisions...)
}
