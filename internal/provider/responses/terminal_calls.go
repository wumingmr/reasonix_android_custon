package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"reasonix/internal/provider"
)

// ErrInvalidFunctionCall identifies a malformed function call in completed output.
var ErrInvalidFunctionCall = errors.New("responses: completed response contains an invalid function call")

// ErrUnfinishedFunctionCall identifies a stream that ended with a pending call.
var ErrUnfinishedFunctionCall = errors.New("responses: stream ended with an unfinished function call")

// Terminal failures invalidate the whole speculative attempt. Never execute a
// readable neighbor while silently dropping a malformed function-call item.
func checkTerminalCalls(ctx context.Context, out chan<- provider.Chunk, response *sseResponse, calls map[string]*streamedCall) bool {
	if err := terminalCallsError(response, calls); err != nil {
		_ = sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkError, Err: err})
		return false
	}
	return true
}

func terminalCallsError(response *sseResponse, calls map[string]*streamedCall) error {
	if response != nil {
		for _, raw := range response.Output {
			var envelope struct {
				Type      string          `json:"type"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(raw, &envelope) != nil || envelope.Type != "function_call" {
				continue
			}
			var item sseItem
			if (len(envelope.Arguments) == 0 || envelope.Arguments[0] != '"') || json.Unmarshal(raw, &item) != nil || item.CallID == "" || item.Name == "" || (item.Status != "" && item.Status != "completed") {
				return ErrInvalidFunctionCall
			}
		}
	}
	closed := make(map[string]bool, len(calls))
	for _, call := range calls {
		if call.completed && call.id != "" {
			closed[call.id] = true
		}
	}
	for _, call := range calls {
		// Unidentified deltas may be recovered by the terminal output under a new
		// item ID; they do not establish an independent executable call obligation.
		if !call.completed && !closed[call.id] && (call.announced || call.name != "" || ((call.argChars > 0 || call.arguments != "") && len(closed) == 0)) {
			return ErrUnfinishedFunctionCall
		}
	}
	return nil
}

func (c *client) finishResponse(ctx context.Context, out chan<- provider.Chunk, event sseEvent, calls map[string]*streamedCall, callForItem func(string) *streamedCall) (failed, ok bool) {
	if event.Type != "response.failed" {
		for _, item := range unclosedOutputCalls(event.Response, calls) {
			if !finishFunctionCall(ctx, out, callForItem(item.ID), item) {
				return false, false
			}
		}
	}
	if event.Type == "response.incomplete" {
		if !sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkReasoning, ReasoningState: provider.ReasoningIncomplete}) {
			return false, false
		}
	}
	if !emitTerminalResponseUsage(ctx, out, event) {
		return false, false
	}
	if event.Type == "response.completed" && !checkTerminalCalls(ctx, out, event.Response, calls) {
		return false, false
	}
	if event.Type != "response.failed" {
		return false, true
	}
	err := fmt.Errorf("responses: response failed")
	if event.Response != nil && event.Response.Error != nil {
		if authErr := authErrorFromResponse(c, event.Response.Error); authErr != nil {
			err = authErr
		} else {
			err = fmt.Errorf("responses: %s", event.Response.Error.Message)
		}
	}
	return true, sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkError, Err: err})
}
