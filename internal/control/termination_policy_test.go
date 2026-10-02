package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"reasonix/internal/provider"
	"reasonix/internal/provider/anthropic"
	"reasonix/internal/provider/openai"
	"reasonix/internal/provider/responses"
	"reasonix/internal/session"
)

func TestTerminationPolicyPreservesSemanticsAndProviderBytes(t *testing.T) {
	system := provider.Message{ID: "system", Role: provider.RoleSystem, Content: "stable system"}
	user := provider.Message{ID: "user", Role: provider.RoleUser, Content: "update files"}
	call := provider.Message{ID: "assistant", Role: provider.RoleAssistant, Content: "calling", ReasoningContent: "original reasoning", ReasoningID: "reason-1", ReasoningStatus: "completed", ReasoningSignature: "original-proof", ToolCalls: []provider.ToolCall{{ID: "done", Name: "lookup", Arguments: `{"q":"value"}`, ThoughtSignature: "thought-proof"}}}
	result := provider.Message{ID: "result", Role: provider.RoleTool, ToolCallID: "done", Name: "lookup", Content: "result", ToolRunState: provider.ToolRunCompleted}
	partial := provider.Message{ID: "partial", Role: provider.RoleAssistant, Content: "unfinished", ReasoningContent: "partial reasoning", ReasoningSignature: "partial-proof"}
	summary := provider.Message{ID: "summary", Role: provider.RoleUser, Content: "<compaction-summary>\nearlier work\n</compaction-summary>"}
	local := provider.Message{ID: "local", Role: provider.RoleTool, Content: "old partial", LocalOnly: true, ToolCallID: provider.LocalOnlyToolID, Name: provider.LocalOnlyToolName, InterruptedTurn: &provider.InterruptedTurnRecovery{Pending: true, DroppedPartialText: true}}
	partialBatch := call
	partialBatch.ToolCalls = append(append([]provider.ToolCall{}, call.ToolCalls...), provider.ToolCall{ID: "unknown", Name: "lookup", Arguments: `{}`})
	fixtures := []struct {
		name             string
		input, expected  []provider.Message
		fallback         provider.Message
		replay           bool
		droppedReasoning bool
	}{
		{name: "partial-reasoning", input: []provider.Message{system, user, partial}, expected: []provider.Message{system, user}, replay: true, droppedReasoning: true},
		{name: "paired-tool-round", input: []provider.Message{system, user, call, result, partial}, expected: []provider.Message{system, user, call, result}, replay: true, droppedReasoning: true},
		{name: "partial-tool-batch", input: []provider.Message{system, user, partialBatch, result}, expected: []provider.Message{system, user}, replay: true, droppedReasoning: true},
		{name: "unreplayable-pair", input: []provider.Message{system, user, call, result}, expected: []provider.Message{system, user}, replay: false, droppedReasoning: true},
		{name: "compaction-and-partial", input: []provider.Message{system, user, summary, call, result, partial}, expected: []provider.Message{system, user, summary, call, result}, replay: true, droppedReasoning: true},
		{name: "existing-local-only", input: []provider.Message{system, user, local}, expected: []provider.Message{system, user}, replay: true},
		{name: "pre-executor-fallback", input: []provider.Message{system}, expected: []provider.Message{system, user}, fallback: user, replay: true},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			before, err := json.Marshal(fixture.input)
			if err != nil {
				t.Fatal(err)
			}
			fallbackBefore, _ := json.Marshal(fixture.fallback)
			got := planCancelledMessages(fixture.input, 1, fixture.fallback, time.Time{}, func(provider.Message) bool { return fixture.replay }, nil)
			after, _ := json.Marshal(fixture.input)
			fallbackAfter, _ := json.Marshal(fixture.fallback)
			if !bytes.Equal(before, after) || !bytes.Equal(fallbackBefore, fallbackAfter) {
				t.Fatal("planner mutated its inputs")
			}
			expected := provider.ModelMessages(fixture.expected)
			if !reflect.DeepEqual(provider.ModelMessages(got), expected) {
				t.Fatalf("model projection differs from explicit legacy-policy fixture:\ngot=%+v\nwant=%+v", provider.ModelMessages(got), expected)
			}
			if len(got) == 0 || got[len(got)-1].InterruptedTurn == nil {
				t.Fatalf("missing recovery handoff: %+v", got)
			}
			recovery := got[len(got)-1].InterruptedTurn
			if !recovery.Pending || recovery.DroppedPartialReasoning != fixture.droppedReasoning {
				t.Fatalf("recovery=%+v", recovery)
			}
			if fixture.name == "paired-tool-round" && len(recovery.CompletedTools) != 1 {
				t.Fatalf("lost completed tool fact: %+v", recovery)
			}
			if fixture.name == "partial-tool-batch" && (len(recovery.CompletedTools) != 1 || len(recovery.UnknownTools) != 1) {
				t.Fatalf("partial batch facts: %+v", recovery)
			}
			reopenedMessages := persistTerminationPolicyModel(t, got)
			for _, adapter := range []string{"openai", "anthropic", "responses"} {
				t.Run(adapter, func(t *testing.T) {
					var mu sync.Mutex
					var bodies [][]byte
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, _ := io.ReadAll(r.Body)
						mu.Lock()
						bodies = append(bodies, body)
						mu.Unlock()
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":{"message":"captured"}}`)
					}))
					defer server.Close()
					var p provider.Provider
					switch adapter {
					case "openai":
						p, err = openai.New(provider.Config{Name: "openai", BaseURL: server.URL, Model: "cache-model", APIKey: "test"})
					case "anthropic":
						p, err = anthropic.New(provider.Config{Name: "anthropic", BaseURL: server.URL, Model: "cache-model", APIKey: "test"})
					case "responses":
						p = responses.New(responses.Config{Name: "responses", BaseURL: server.URL, Model: "cache-model", APIKey: "test", Mode: "stateless"})
					}
					if err != nil {
						t.Fatal(err)
					}
					for _, messages := range [][]provider.Message{fixture.expected, got, reopenedMessages} {
						stream, err := p.Stream(t.Context(), provider.Request{Messages: messages, Tools: []provider.ToolSchema{{Name: "lookup", Description: "look up", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}}})
						if err == nil {
							for range stream {
							}
						}
					}
					mu.Lock()
					defer mu.Unlock()
					if len(bodies) != 3 {
						t.Fatalf("captured %d requests, want 3", len(bodies))
					}
					if !bytes.Equal(bodies[0], bodies[1]) {
						t.Fatalf("provider bytes changed:\nexpected:%s\nactual:%s", bodies[0], bodies[1])
					}
					if !bytes.Equal(bodies[0], bodies[2]) {
						t.Fatalf("provider bytes changed after reopen:\nexpected:%s\nactual:%s", bodies[0], bodies[2])
					}
				})
			}
		})
	}
}

func persistTerminationPolicyModel(t *testing.T, messages []provider.Message) []provider.Message {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "termination-policy")
	store, err := session.CreateStore(dir, "termination-policy")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"messages": messages})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(t.Context(), session.Batch{OperationID: "model-cleanup", Events: []session.Event{{Kind: "model/context-replace", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := session.Open(dir, "termination-policy")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	return reopened.DeriveMessages()
}

func TestTerminationPolicyFallbackOwnsImageSlice(t *testing.T) {
	fallback := provider.Message{ID: "user", Role: provider.RoleUser, Content: "inspect image", Images: []string{"original"}}
	got := planCancelledMessages(nil, 0, fallback, time.Time{}, func(provider.Message) bool { return true }, nil)
	if len(got) < 1 || len(got[0].Images) != 1 {
		t.Fatalf("fallback missing: %+v", got)
	}
	got[0].Images[0] = "changed"
	if fallback.Images[0] != "original" {
		t.Fatal("fallback image slice aliases input")
	}
}
