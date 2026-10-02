package agent

import "reasonix/internal/plancontract"

// SetPlanContract records the approved plan this turn executes, or clears it
// when the turn runs without one. The coordinator sets it before every executor
// run so a turn never inherits the previous turn's plan.
func (a *Agent) SetPlanContract(plan *plancontract.Plan) {
	if a == nil {
		return
	}
	a.sess.todoMu.Lock()
	defer a.sess.todoMu.Unlock()
	if plan == nil {
		a.planContract = nil
		return
	}
	copied := *plan
	a.planContract = &copied
}

func (a *Agent) planContractSnapshot() *plancontract.Plan {
	if a == nil {
		return nil
	}
	a.sess.todoMu.Lock()
	defer a.sess.todoMu.Unlock()
	if a.planContract == nil {
		return nil
	}
	copied := *a.planContract
	return &copied
}
