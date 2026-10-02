package control

import (
	"context"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// The approved-plan execution turn runs in the planning turn's context, which
// carries the user message identity the controller reserved for that turn.
func TestApprovedPlanExecutionMintsItsOwnUserMessageID(t *testing.T) {
	prov := &scriptedTurns{turns: planThenExecuteTurns("Plan:\n1. Change it", "Done.")}
	ag := newPlanTestAgent(prov)
	approvalID := make(chan string, 1)
	c := newOwnedTestController(t, Options{
		Runner:   ag,
		Executor: ag,
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.ApprovalRequest {
				approvalID <- e.Approval.ID
			}
		}),
	})
	c.SetPlanMode(true)
	go func() { c.Approve(<-approvalID, true, false, false) }()

	if err := c.runTurnWithRaw(context.Background(), "change it", "change it"); err != nil {
		t.Fatalf("runTurnWithRaw: %v", err)
	}
	users := 0
	seen := map[string]bool{}
	for _, message := range ag.Session().Messages {
		if message.Role == provider.RoleUser {
			users++
		}
		if seen[message.ID] {
			t.Fatalf("message id %q appears twice (second as %s/%s)", message.ID, message.Role, message.Origin)
		}
		seen[message.ID] = true
	}
	if users < 2 {
		t.Fatalf("user messages = %d, want the request and the approved-plan continuation", users)
	}
}
