package transcript

import (
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

func TestHTTP2FailureHistoryKeepsTransportIdentity(t *testing.T) {
	rows := History([]provider.Message{{ID: "failure", Role: provider.RoleTool, LocalOnly: true,
		ToolCallID: provider.LocalOnlyToolID, Name: provider.LocalOnlyToolName,
		InterruptedTurn: &provider.InterruptedTurnRecovery{Pending: true, TerminalStatus: "failed",
			FailureDiagnostic: &provider.FailureDiagnostic{Kind: provider.FailureKindTransportProtocol, TransportCode: "PROTOCOL_ERROR", ProviderID: "saved-provider", ProviderDisplayName: "DeepSeek", Protocol: "openai"}}}}, HistoryOptions{})
	if len(rows) != 1 || rows[0].Code != event.NoticeCodeProviderRequestFailed || rows[0].Level != "warn" || rows[0].Diagnostic == nil || rows[0].Diagnostic.Kind != provider.FailureKindTransportProtocol || rows[0].Diagnostic.TransportCode != "PROTOCOL_ERROR" || !strings.Contains(rows[0].Detail, "saved-provider") {
		t.Fatalf("history lost failure classification: %+v", rows)
	}
	if strings.Contains(rows[0].Detail, "PROTOCOL_ERROR") {
		t.Fatal("history duplicated the structured transport code in prose")
	}
}

func TestHistoryIdentityAndLegacyPresentation(t *testing.T) {
	raw := "[Pasted text #1 · 2 lines]\n--- Begin [Pasted text #1 · 2 lines] ---\none\ntwo\n--- End [Pasted text #1 · 2 lines] ---"
	messages := []provider.Message{{ID: "user", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: raw, RawContent: raw},
		{ID: "plan", Role: provider.RoleAssistant, Content: "plan and its final note", ToolCalls: []provider.ToolCall{{ID: "call", Name: "proxy", ResolvedName: "read_file", CapabilityID: "read", Arguments: "full args", Diff: "diff", Added: 1}}}}
	rows := History(messages, HistoryOptions{SubmitContent: func(m provider.Message) string { return m.Content }, LegacyTurns: []LegacyDisplayTurn{{TurnID: "turn", UserMessageID: "user", Messages: []Message{{MessageID: "plan", Role: "assistant", Content: "plan"}}}}})
	if len(rows) != 2 || rows[0].Content != "[Pasted text #1 · 2 lines]" || rows[0].SubmitText != raw {
		t.Fatalf("legacy display/replay mismatch: %+v", rows)
	}
	if rows[1].RecordID != "m:plan" || rows[1].Content != "plan and its final note" {
		t.Fatalf("identified planner duplicated: %+v", rows)
	}
	call := rows[1].ToolCalls[0]
	if call.ResolvedName != "read_file" || call.CapabilityID != "read" || call.Arguments != "full args" || call.Diff != "diff" {
		t.Fatalf("tool metadata lost: %+v", call)
	}
}

func TestHistorySteersRetainDistinctCanonicalMessageIdentities(t *testing.T) {
	const content = "[Mid-turn steer queued by the user. Do not treat this as a new task; use it only as additional guidance for the current task after completing the current step.]\nSame guidance"
	rows := History([]provider.Message{
		{ID: "first", Role: provider.RoleUser, Content: content},
		{ID: "second", Role: provider.RoleUser, Content: content},
	}, HistoryOptions{})
	if len(rows) != 2 {
		t.Fatalf("same-text steers collapsed: %+v", rows)
	}
	for i, id := range []string{"first", "second"} {
		if rows[i].MessageID != id || rows[i].RecordID != "m:"+id || rows[i].Content != "↪ Same guidance" || rows[i].Role != "notice" {
			t.Fatalf("steer lost canonical identity: %+v", rows[i])
		}
	}
}

func TestHistoryUnappliedSteerKeepsLegacyRecordAddressAndMessageIdentity(t *testing.T) {
	const content = "[Mid-turn steer queued by the user. Do not treat this as a new task; use it only as additional guidance for the current task after completing the current step.]\nUse plan B"
	rows := History([]provider.Message{{ID: "queued", Role: provider.RoleTool, LocalOnly: true,
		ToolCallID: provider.LocalOnlyToolID, Name: provider.LocalOnlyToolName, Content: content}}, HistoryOptions{})
	if len(rows) != 1 || rows[0].Role != "notice" || rows[0].Code != event.NoticeCodeUnappliedSteer ||
		rows[0].MessageID != "queued" || rows[0].RecordID != "m:queued:notice:0" {
		t.Fatalf("unapplied steer identity = %+v", rows)
	}
}

func TestHistoryCancelledLegacySuppressesCanonicalLocalBody(t *testing.T) {
	rows := History([]provider.Message{{ID: "u", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: "q"},
		{ID: "partial", Role: provider.RoleAssistant, LocalOnly: true, Content: "must not duplicate"},
		{ID: "receipt", Role: provider.RoleAssistant, LocalOnly: true, DecisionReceipt: &provider.DecisionReceipt{ID: "decision"}}},
		HistoryOptions{LegacyTurns: []LegacyDisplayTurn{{UserMessageID: "u", TurnID: "t", Messages: []Message{{Role: "assistant", Content: "partial display"}, {Role: "notice", Code: event.NoticeCodeCancelledTurn}}}}})
	if len(rows) != 4 {
		t.Fatalf("rows=%+v", rows)
	}
	for _, row := range rows {
		if strings.Contains(row.Content, "must not duplicate") {
			t.Fatal("canonical cancelled body duplicated")
		}
	}
	if rows[3].DecisionReceipt == nil {
		t.Fatal("independent receipt lost")
	}
}
