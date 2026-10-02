package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func retiredRecoveryFixture() *Agent {
	record := provider.ToolCallRecord{
		Identity:  provider.ActionIdentity{CallID: "old", AttemptID: "attempt", CanonicalTool: "external_write"},
		State:     provider.ToolRunUnknown,
		Arguments: json.RawMessage(`{"value":"kept"}`),
	}
	session := NewSession("")
	session.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "old", Name: "external_write", Recovery: &record}}})
	return New(nil, tool.NewRegistry(), session, Options{}, event.Discard)
}

func TestRetiredToolRecoveryActionsNeverRewriteOrReplay(t *testing.T) {
	a := retiredRecoveryFixture()
	fact, err := a.InspectToolRecovery(context.Background(), "attempt")
	if err == nil || !strings.Contains(err.Error(), "tool_recovery_retired") || fact.State != provider.ToolRunUnknown {
		t.Fatalf("inspect fact=%+v err=%v", fact, err)
	}
	if err := a.ResolveToolRecovery("attempt", "inspection", "confirm"); err == nil || !strings.Contains(err.Error(), "tool_recovery_retired") {
		t.Fatalf("resolve err=%v", err)
	}
	if err := a.RetryToolRecovery(context.Background(), "attempt", "inspection"); err == nil || !strings.Contains(err.Error(), "tool_recovery_retired") {
		t.Fatalf("retry err=%v", err)
	}
	pending := a.PendingToolRecovery()
	if len(pending) != 1 || pending[0].State != provider.ToolRunUnknown || string(pending[0].Arguments) != `{"value":"kept"}` {
		t.Fatalf("historical record changed: %+v", pending)
	}
}
