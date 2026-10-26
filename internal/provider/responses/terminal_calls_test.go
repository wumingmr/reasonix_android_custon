package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func TestTerminalCallsRejectUnsafeCompletion(t *testing.T) {
	for _, tc := range []struct{ name, output string }{
		{"arguments object", `{"type":"function_call","call_id":"call_1","name":"echo","arguments":{"secret":"must not appear"}}`},
		{"arguments null", `{"type":"function_call","call_id":"call_1","name":"echo","arguments":null}`},
		{"arguments missing", `{"type":"function_call","call_id":"call_1","name":"echo"}`},
		{"missing call id", `{"type":"function_call","name":"echo","arguments":"{}"}`},
		{"missing name", `{"type":"function_call","call_id":"call_1","arguments":"{}"}`},
		{"in_progress item at completed terminal", `{"type":"function_call","call_id":"call_1","name":"echo","status":"in_progress","arguments":"{}"}`},
		{"incomplete item at completed terminal", `{"type":"function_call","call_id":"call_1","name":"echo","status":"incomplete","arguments":"{}"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks := chunksOf(t, `{"type":"response.output_text.delta","delta":"next step"}`, `{"type":"response.completed","response":{"id":"r","output":[`+tc.output+`]}}`)
			var failed bool
			for _, chunk := range chunks {
				switch chunk.Type {
				case provider.ChunkError:
					failed = true
					if !errors.Is(chunk.Err, ErrInvalidFunctionCall) || !errors.Is(fmt.Errorf("consumer: %w", chunk.Err), ErrInvalidFunctionCall) {
						t.Errorf("invalid-call identity lost: %v", chunk.Err)
					}
					if errors.Is(chunk.Err, ErrUnfinishedFunctionCall) {
						t.Error("protocol causes must stay distinct")
					}
					if provider.IsStreamInterrupted(chunk.Err) || provider.ClassifyRecovery(chunk.Err).Retryable {
						t.Errorf("protocol error became retryable: %v", chunk.Err)
					}
					if strings.Contains(chunk.Err.Error(), "secret") {
						t.Fatal("error leaked arguments")
					}
				case provider.ChunkDone:
					t.Error("unsafe terminal emitted done")
				}
			}
			if !failed {
				t.Fatal("unsafe terminal silently succeeded")
			}
		})
	}
}

func TestTerminalCallsRecognizeAlternateItemIdentity(t *testing.T) {
	calls := map[string]*streamedCall{
		"original": {id: "call_1", name: "echo"},
		"terminal": {id: "call_1", name: "echo", arguments: "{}", completed: true},
		"":         {arguments: "{}"},
	}
	if err := terminalCallsError(nil, calls); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalCallsKeepTextOnlyDONE(t *testing.T) {
	chunks := chunksOf(t, `{"type":"response.output_text.delta","delta":"I will open the browser"}`, "[DONE]")
	var done bool
	for _, chunk := range chunks {
		if chunk.Type == provider.ChunkError {
			t.Fatal(chunk.Err)
		}
		done = done || chunk.Type == provider.ChunkDone
	}
	if !done {
		t.Fatal("text-only DONE compatibility lost")
	}
}

func TestTerminalCallsLeaveArgumentJSONToToolValidation(t *testing.T) {
	// A string's JSON syntax is an existing tool-validation concern. This guard
	// rejects wrong wire types rather than inventing a second argument repairer.
	response := &sseResponse{Output: []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"echo","arguments":"{"}`)}}
	calls := map[string]*streamedCall{"fc_1": {id: "call_1", name: "echo", arguments: "{", completed: true}}
	if err := terminalCallsError(response, calls); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalCallsCanceledSendDoesNotBlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if checkTerminalCalls(ctx, make(chan provider.Chunk), nil, map[string]*streamedCall{"fc_1": {id: "call_1", name: "echo"}}) {
		t.Fatal("unfinished call accepted")
	}
}

func TestTerminalCallsRecoverOrphanDeltaWithDifferentItemID(t *testing.T) {
	calls := toolCallsOf(t,
		`{"type":"response.function_call_arguments.delta","item_id":"old_item","delta":"{}"}`,
		`{"type":"response.completed","response":{"id":"r","output":[{"type":"function_call","id":"new_item","call_id":"call_1","name":"echo","arguments":"{}"}]}}`)
	if len(calls) != 1 || calls[0].ID != "call_1" {
		t.Fatalf("calls=%+v", calls)
	}
	for _, chunk := range chunksOf(t,
		`{"type":"response.function_call_arguments.delta","item_id":"old_item","delta":"{}"}`,
		`{"type":"response.completed","response":{"id":"r","output":[{"type":"function_call","id":"new_item","call_id":"call_1","name":"echo","arguments":"{}"}]}}`) {
		if chunk.Type == provider.ChunkError {
			t.Fatal(chunk.Err)
		}
	}
}

func TestTerminalCallsRejectArgumentsDoneWithoutIdentity(t *testing.T) {
	for _, terminal := range []string{"[DONE]", `{"type":"response.completed","response":{"id":"r","output":[]}}`} {
		failed := false
		for _, chunk := range chunksOf(t, `{"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{}","name":"echo"}`, terminal) {
			if chunk.Type == provider.ChunkError {
				failed = true
				if !errors.Is(chunk.Err, ErrUnfinishedFunctionCall) || !errors.Is(fmt.Errorf("consumer: %w", chunk.Err), ErrUnfinishedFunctionCall) {
					t.Errorf("unfinished-call identity lost: %v", chunk.Err)
				}
				if errors.Is(chunk.Err, ErrInvalidFunctionCall) {
					t.Error("protocol causes must stay distinct")
				}
			}
			if chunk.Type == provider.ChunkDone {
				t.Error("unnamed call silently completed")
			}
		}
		if !failed {
			t.Fatal("want explicit protocol error")
		}
	}
}
