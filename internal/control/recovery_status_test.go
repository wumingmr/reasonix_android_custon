package control

import (
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func TestUnknownToolEffectIsFactOnlyInterruptedStatus(t *testing.T) {
	a := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	a.Session().Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
		ID:   "external-1",
		Name: "external_write",
		Recovery: &provider.ToolCallRecord{
			Identity: provider.ActionIdentity{AttemptID: "attempt-1", CallID: "external-1", CanonicalTool: "external_write"},
			State:    provider.ToolRunUnknown,
		},
	}}})
	c := &Controller{executor: a}
	done := event.Event{Cancelled: true}

	c.applyToolRecoveryTurnStatus(&done, nil)

	if done.Recovery == nil || done.Recovery.State != "unknown" || done.Recovery.Reason != "tool_effect_unconfirmed" {
		t.Fatalf("recovery = %+v, want fact-only unknown status", done.Recovery)
	}
	if done.Recovery.RequiresUserDecision {
		t.Fatal("unknown tool effect must not require a recovery decision")
	}
	if got := terminalTurnStatus(done); got != event.TurnInterrupted {
		t.Fatalf("terminal status = %q, want %q", got, event.TurnInterrupted)
	}
}
