package provider

import (
	"encoding/json"
	"strings"
)

// RepairHistoryForReplay turns unusable historical tool batches into ordinary
// records, never executable calls. Unlike replacing unknown arguments with {},
// this preserves uncertainty about side effects and keeps recorded results.
// Use only for host history, before extensions and the final transcript gate.
// Canonical messages are not mutated; healthy wire bytes remain unchanged.
func RepairHistoryForReplay(msgs []Message) []Message {
	out := RepairRejectedArguments(ProjectionMessages(msgs))
	for i := 0; i < len(out); {
		m := out[i]
		if len(m.ToolCalls) == 0 {
			// Unpaired results are still observations, even when their call was
			// lost at an interrupted boundary. Do not silently drop the facts.
			if m.Role == RoleTool {
				out[i] = archivedToolResult(m)
			}
			i++
			continue
		}
		end := i + 1
		for end < len(out) && out[end].Role == RoleTool {
			end++
		}
		if !replayableToolBatch(m) {
			out[i] = archivedToolCalls(m)
			for j := i + 1; j < end; j++ {
				out[j] = archivedToolResult(out[j])
			}
		}
		i = end
	}
	return out
}

func replayableToolBatch(m Message) bool {
	if m.Role != RoleAssistant {
		return false
	}
	seen := make(map[string]bool, len(m.ToolCalls))
	for _, call := range m.ToolCalls {
		if strings.TrimSpace(call.Name) == "" {
			return false
		}
		// Empty IDs retain the existing positional compatibility path.
		if call.ID != "" {
			if strings.TrimSpace(call.ID) == "" || seen[call.ID] {
				return false
			}
			seen[call.ID] = true
		}
		var args map[string]json.RawMessage
		if json.Unmarshal([]byte(call.Arguments), &args) != nil || args == nil {
			return false
		}
	}
	return true
}

func archivedToolCalls(m Message) Message {
	type record struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"original_arguments"`
	}
	calls := make([]record, 0, len(m.ToolCalls))
	for _, call := range m.ToolCalls {
		calls = append(calls, record{call.ID, call.Name, call.Arguments})
	}
	data, _ := json.Marshal(calls) // strings only; no local recovery metadata
	return archivedMessage(m, "[Historical tool record: the original call format cannot be replayed. These are archived proposals, not new tool calls. Execution is unconfirmed unless the recorded results establish it. Do not repeat actions solely because this record was recovered; verify their effects first.]\n"+string(data))
}

func archivedToolResult(m Message) Message {
	data, _ := json.Marshal(struct {
		CallID string       `json:"call_id"`
		Name   string       `json:"name"`
		State  ToolRunState `json:"recorded_execution_state,omitempty"`
	}{m.ToolCallID, m.Name, m.ToolRunState})
	// Content is the existing provider-visible observation, never RawContent.
	return archivedMessage(m, "[Historical tool observation; pairing may be incomplete. This record does not request a new execution.]\n"+string(data))
}

func archivedMessage(m Message, note string) Message {
	content := note
	if m.Content != "" {
		data, _ := json.Marshal(struct {
			Content string `json:"original_message_content"`
		}{m.Content})
		content += "\n" + string(data)
	}
	// Drop native tool replay metadata. Host-authored user data preserves images
	// and avoids assistant prefill, which thinking providers may reject.
	return Message{ID: m.ID, Role: RoleUser, Origin: MessageOriginHost, Content: content, Images: m.Images, ImageInputs: m.ImageInputs}
}
