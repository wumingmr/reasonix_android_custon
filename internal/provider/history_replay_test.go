package provider

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestHistoryReplayRecoversMalformedBatchesWithoutMutatingEvidence(t *testing.T) {
	for _, args := range []string{`{"body":"quote ["text"]"}`, "", "null", "[]", `"value"`} {
		for _, state := range []ToolRunState{"", ToolRunUnknown, ToolRunCompleted, ToolRunNotStarted, ToolRunCancelled} {
			t.Run(args+"/"+string(state), func(t *testing.T) {
				messages := []Message{
					{ID: "proposal", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call", Name: "write_file", Arguments: args}}, ReasoningSignature: "private-signature"},
					{ID: "result", Role: RoleTool, ToolCallID: "call", Name: "write_file", Content: "recorded result", ToolRunState: state, RawContent: "private-raw"},
					{ID: "next", Role: RoleUser, Content: "continue"},
				}
				before, _ := json.Marshal(messages)
				repaired := RepairHistoryForReplay(messages)
				if err := ValidateModelTranscript(repaired); err != nil {
					t.Fatal(err)
				}
				if state == ToolRunNotStarted || state == ToolRunCancelled {
					if repaired[0].ToolCalls[0].Arguments != "{}" {
						t.Fatal("refused call was not repaired")
					}
				} else if len(repaired[0].ToolCalls) != 0 || repaired[1].Role != RoleUser || !strings.Contains(repaired[1].Content, "recorded result") {
					t.Fatal("unknown/executed call was not retained as historical observations")
				}
				wire, _ := json.Marshal(ModelMessages(repaired))
				if strings.Contains(string(wire), "private-raw") {
					t.Fatal("local metadata leaked")
				}
				if !reflect.DeepEqual(repaired, RepairHistoryForReplay(repaired)) {
					t.Fatal("repair is not idempotent")
				}
				after, _ := json.Marshal(messages)
				if !bytes.Equal(before, after) {
					t.Fatal("canonical evidence changed")
				}
			})
		}
	}
}

func TestHistoryReplayIdentityAndImageRecovery(t *testing.T) {
	for _, calls := range [][]ToolCall{
		{{ID: "same", Name: "read", Arguments: "{}"}, {ID: "same", Name: "read", Arguments: "{}"}},
		{{ID: "call", Name: "", Arguments: "{}"}},
		{{ID: " ", Name: "read", Arguments: "{}"}},
	} {
		messages := []Message{
			{Role: RoleAssistant, ToolCalls: calls, ResponsesItems: []json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"old"}`)}},
			{Role: RoleTool, ToolCallID: calls[0].ID, Name: "read", Content: "observation", Images: []string{"https://example.invalid/image.png"}},
		}
		got := RepairHistoryForReplay(messages)
		if err := ValidateModelTranscript(got); err != nil {
			t.Fatal(err)
		}
		if len(got[0].ResponsesItems) != 0 || got[1].Role != RoleUser || len(got[1].Images) != 1 {
			t.Fatal("native replay or images were not safely converted")
		}
	}
	orphan := Message{Role: RoleTool, ToolCallID: "missing", Name: "read", Content: "orphan observation"}
	if got := RepairHistoryForReplay([]Message{orphan}); len(got) != 1 || !strings.Contains(got[0].Content, orphan.Content) {
		t.Fatal("orphan facts were lost")
	}
}

func TestHistoryReplayPreservesHealthyWireBytes(t *testing.T) {
	messages := []Message{
		{Role: RoleSystem, Content: "stable system"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call", Name: "read", Arguments: ` { "path": "one" } `}}, ReasoningContent: "thinking"},
		{Role: RoleTool, ToolCallID: "call", Name: "read", Content: "result", ToolRunState: ToolRunCompleted},
		{Role: RoleUser, Content: "continue"},
	}
	before, _ := json.Marshal(ModelMessages(messages))
	after, _ := json.Marshal(ModelMessages(RepairHistoryForReplay(messages)))
	if !bytes.Equal(before, after) {
		t.Fatalf("healthy prefix changed:\n%s\n%s", before, after)
	}
}
