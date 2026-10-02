package responses

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"reasonix/internal/provider"
)

func chunksOf(t *testing.T, events ...string) []provider.Chunk {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeEvents(w, events...)
	}))
	defer server.Close()
	return collect(t, New(Config{Name: "compatible", APIKey: "k", BaseURL: server.URL, Model: "m", Mode: "stateless"}),
		provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "list files"}}})
}

func toolCallsOf(t *testing.T, events ...string) []provider.ToolCall {
	t.Helper()
	var calls []provider.ToolCall
	for _, chunk := range chunksOf(t, events...) {
		if chunk.Type == provider.ChunkToolCall && chunk.ToolCall != nil {
			calls = append(calls, *chunk.ToolCall)
		}
	}
	return calls
}

func TestCompletedOutputClosesACallWhoseDoneEventsNeverCame(t *testing.T) {
	calls := toolCallsOf(t,
		`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"ls","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"path\":"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"\".\"}"}`,
		`{"type":"response.completed","response":{"id":"resp_1","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"ls","arguments":"{\"path\":\".\"}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "ls" || calls[0].Arguments != `{"path":"."}` {
		t.Fatalf("calls = %#v, want the one call the completed response lists", calls)
	}
}

func TestCompletedOutputClosesACallWhoseDeltasCarriedNoItemID(t *testing.T) {
	calls := toolCallsOf(t,
		`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"ls","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{}"}`,
		`{"type":"response.completed","response":{"id":"resp_1","output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"ls","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "ls" {
		t.Fatalf("calls = %#v, want exactly the listed call", calls)
	}
}

func TestCompletedOutputDoesNotRepeatACallTheStreamClosed(t *testing.T) {
	calls := toolCallsOf(t,
		`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"ls","arguments":""}}`,
		`{"type":"response.output_item.done","item":{"id":"fc_2","type":"function_call","status":"completed","call_id":"call_1","name":"ls","arguments":"{}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"ls","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)
	if len(calls) != 1 || calls[0].ID != "call_1" {
		t.Fatalf("calls = %#v, want call_1 dispatched once", calls)
	}
}

func TestIncompleteOutputCallIsNotDispatched(t *testing.T) {
	calls := toolCallsOf(t,
		`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"write_file","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"path\":\"a"}`,
		`{"type":"response.incomplete","response":{"id":"resp_1","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"fc_1","type":"function_call","status":"incomplete","call_id":"call_1","name":"write_file","arguments":"{\"path\":\"a"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)
	if len(calls) != 0 {
		t.Fatalf("calls = %#v, want a truncated call left undispatched", calls)
	}
}

func TestCompletedOutputSurvivesAnItemItCannotRead(t *testing.T) {
	chunks := chunksOf(t,
		`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"ls","arguments":""}}`,
		`{"type":"response.completed","response":{"id":"resp_1","output":[{"id":"rs_1","type":"reasoning","status":{"phase":"done"}},{"id":"fc_2","type":"function_call","call_id":"call_2","name":"ls","arguments":{"path":"."}},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"ls","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)
	var calls []provider.ToolCall
	done := false
	for _, chunk := range chunks {
		switch chunk.Type {
		case provider.ChunkError:
			t.Fatalf("error chunk: %v", chunk.Err)
		case provider.ChunkDone:
			done = true
		case provider.ChunkToolCall:
			calls = append(calls, *chunk.ToolCall)
		}
	}
	if !done {
		t.Fatalf("chunks = %#v, want the turn to finish", chunks)
	}
	if len(calls) != 1 || calls[0].ID != "call_1" {
		t.Fatalf("calls = %#v, want only the readable call_1", calls)
	}
}

func TestOutputCallWithoutCallIDIsNotDispatched(t *testing.T) {
	calls := toolCallsOf(t,
		`{"type":"response.completed","response":{"id":"resp_1","output":[{"id":"fc_1","type":"function_call","status":"completed","name":"ls","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)
	if len(calls) != 0 {
		t.Fatalf("calls = %#v, want a call with no call id left undispatched", calls)
	}
}

func TestOutputListingACallTwiceDispatchesItOnce(t *testing.T) {
	calls := toolCallsOf(t,
		`{"type":"response.completed","response":{"id":"resp_1","output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"ls","arguments":"{}"},{"id":"fc_2","type":"function_call","call_id":"call_1","name":"ls","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)
	if len(calls) != 1 || calls[0].ID != "call_1" {
		t.Fatalf("calls = %#v, want call_1 dispatched once", calls)
	}
}

func TestArgumentsDoneWithoutItemIDIsNotDispatched(t *testing.T) {
	calls := toolCallsOf(t,
		`{"type":"response.function_call_arguments.done","arguments":"{}"}`,
		`{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	)
	if len(calls) != 0 {
		t.Fatalf("calls = %#v, want no call without a name", calls)
	}
}
