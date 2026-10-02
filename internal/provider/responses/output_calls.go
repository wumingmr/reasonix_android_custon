package responses

import (
	"cmp"
	"context"
	"encoding/json"

	"reasonix/internal/provider"
)

// finishFunctionCall completes call from a finished function_call item, sending
// it once however many sources report it finished.
func finishFunctionCall(ctx context.Context, out chan<- provider.Chunk, call *streamedCall, item *sseItem) bool {
	call.id = cmp.Or(item.CallID, call.id)
	call.name = cmp.Or(item.Name, call.name)
	call.arguments = cmp.Or(item.Arguments, call.arguments)
	return completeFunctionCall(ctx, out, call)
}

// completeFunctionCall sends call once. One with no name yet, such as an
// arguments.done event that named no item, stays open for a later event to name.
func completeFunctionCall(ctx context.Context, out chan<- provider.Chunk, call *streamedCall) bool {
	if call.completed || call.name == "" {
		return true
	}
	call.completed = true
	return sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: call.id, Name: call.name, Arguments: call.arguments}})
}

// outputItems decodes a terminal response's output one item at a time, so an
// item a relay sent in a shape the protocol does not define is dropped alone
// instead of taking the whole terminal event with it.
func outputItems(response *sseResponse) []sseItem {
	if response == nil {
		return nil
	}
	items := make([]sseItem, 0, len(response.Output))
	for _, raw := range response.Output {
		var item sseItem
		if json.Unmarshal(raw, &item) == nil {
			items = append(items, item)
		}
	}
	return items
}

// unclosedOutputCalls lists the function calls a terminal response's output
// reports but the stream never closed: that list is the response's own account
// of what it issued. A call already closed or listed under the same call id is
// left out, and so is one the response marks unfinished or lists without a call id.
func unclosedOutputCalls(response *sseResponse, calls map[string]*streamedCall) []*sseItem {
	closed := make(map[string]bool, len(calls))
	for _, call := range calls {
		if call.completed && call.id != "" {
			closed[call.id] = true
		}
	}
	var out []*sseItem
	items := outputItems(response)
	for i := range items {
		item := &items[i]
		if item.Type != "function_call" || item.CallID == "" ||
			(item.Status != "" && item.Status != "completed") || closed[item.CallID] {
			continue
		}
		closed[item.CallID] = true
		out = append(out, item)
	}
	return out
}
