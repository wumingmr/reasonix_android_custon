package session_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
	"reasonix/internal/provider/anthropic"
	"reasonix/internal/provider/openai"
	"reasonix/internal/provider/responses"
	"reasonix/internal/session"
)

func TestProviderRequestBytesSurviveSessionV4RoundTrip(t *testing.T) {
	messages := []provider.Message{
		{ID: "system", Role: provider.RoleSystem, Content: "stable system"},
		{ID: "user", Role: provider.RoleUser, Content: string(bytes.Repeat([]byte("cache-prefix-"), 7000))},
		{ID: "assistant", Role: provider.RoleAssistant, Content: "calling", ReasoningContent: "reason", ReasoningID: "reason-1", ReasoningStatus: "completed", ReasoningSignature: "opaque-signature", ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "lookup", Arguments: `{"q":"value"}`, ThoughtSignature: "thought-proof"}}},
		{ID: "tool", Role: provider.RoleTool, ToolCallID: "call-1", Name: "lookup", Content: "result"},
	}
	after := persistCacheMessages(t, messages)
	tools := []provider.ToolSchema{{Name: "lookup", Description: "look up", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}}

	tests := []struct {
		name string
		new  func(string) provider.Provider
	}{
		{name: "openai", new: func(url string) provider.Provider {
			p, err := openai.New(provider.Config{Name: "openai", BaseURL: url, Model: "cache-model", APIKey: "test"})
			if err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{name: "anthropic", new: func(url string) provider.Provider {
			p, err := anthropic.New(provider.Config{Name: "anthropic", BaseURL: url, Model: "cache-model", APIKey: "test"})
			if err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{name: "responses", new: func(url string) provider.Provider {
			return responses.New(responses.Config{Name: "responses", BaseURL: url, Model: "cache-model", APIKey: "test", Mode: "stateless"})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var bodies [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				bodies = append(bodies, append([]byte(nil), body...))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"message":"captured"}}`)
			}))
			defer server.Close()
			p := test.new(server.URL)
			captureProviderRequest(t, p, provider.Request{Messages: messages, Tools: tools})
			captureProviderRequest(t, p, provider.Request{Messages: after, Tools: tools})
			if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
				t.Fatalf("serialized request changed across v4 persistence: requests=%d\nbefore: %s\nafter:  %s", len(bodies), firstBody(bodies, 0), firstBody(bodies, 1))
			}
		})
	}
}

func captureProviderRequest(t *testing.T, p provider.Provider, request provider.Request) {
	t.Helper()
	stream, err := p.Stream(t.Context(), request)
	if err != nil {
		return
	}
	for range stream {
	}
}

func firstBody(bodies [][]byte, index int) []byte {
	if index < 0 || index >= len(bodies) {
		return nil
	}
	return bodies[index]
}

func persistCacheMessages(t *testing.T, messages []provider.Message) []provider.Message {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cache")
	store, err := session.CreateStore(dir, "cache")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		payload, err := json.Marshal(map[string]any{"message": message})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(t.Context(), session.Batch{OperationID: "message-" + message.ID, Events: []session.Event{{Kind: "message/complete", Payload: payload}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := session.Open(dir, "cache")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	return reopened.DeriveMessages()
}
