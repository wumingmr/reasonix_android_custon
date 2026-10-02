package agent

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/extension"
	"reasonix/internal/extension/dispatch"
	"reasonix/internal/extension/protocol"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func TestSummaryRecoversOversizedProtectedToolResult(t *testing.T) {
	p := &mockProvider{name: "fixture", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Read completed; keep the latest request and use the observation to continue."},
		{Type: provider.ChunkDone},
	}}
	s := NewSession("sys")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "keep the latest request"})
	s.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "large", Name: "read", Arguments: `{}`}}})
	s.Add(provider.Message{Role: provider.RoleTool, ToolCallID: "large", Name: "read", Content: strings.Repeat("large observation ", 10000)})
	before := s.Snapshot()
	// The recent tool result is too large for the desired replay, but fits the
	// summarizer's input window. Recover by summarizing it, never truncating it.
	a := New(p, tool.NewRegistry(), s, Options{ContextWindow: 64000}, event.Discard)
	if err := a.CompactNow(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 1 {
		t.Fatalf("summary requests=%d, want 1", len(p.requests))
	}
	observed := false
	for _, msg := range p.requests[0].Messages {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "large" && msg.Content == before[3].Content {
			observed = true
		}
	}
	if !observed || !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("summary lost the protected tool result or changed canonical history")
	}
	prepared := a.contextManager().currentPrepared()
	if prepared.ProjectionVersion == 0 || prepared.InputTokens >= 5000 || latestDigest(prepared.Messages) == "" {
		t.Fatalf("summary did not recover a compact replay: version=%d tokens=%d", prepared.ProjectionVersion, prepared.InputTokens)
	}
	if err := provider.ValidateModelTranscript(provider.ModelMessages(prepared.Messages)); err != nil {
		t.Fatalf("invalid recovered transcript: %v", err)
	}
	if truncatedRescue(a) {
		t.Fatal("summary recovery used truncation")
	}
}

func TestRecoveredHistoryReachesModelWithoutExecutingTools(t *testing.T) {
	mp := &mockProvider{name: "fixture", chunks: []provider.Chunk{{Type: provider.ChunkDone}}}
	s := NewSession("system")
	s.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "old", Name: "write_file", Arguments: `{"body":"["text"]"}`}}})
	s.Add(provider.Message{Role: provider.RoleTool, ToolCallID: "old", Name: "write_file", ToolRunState: provider.ToolRunUnknown, Content: "original outcome uncertain"})
	s.Add(provider.Message{Role: provider.RoleUser, Content: "continue"})
	a := New(mp, tool.NewRegistry(), s, Options{}, event.Discard)
	req, err := a.buildSamplingRequest(t.Context(), CompactionTriggerPressure)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := a.streamProviderRequest(t.Context(), req.req)
	if err != nil {
		t.Fatal(err)
	}
	for range stream {
	}
	if len(mp.requests) != 1 || s.Snapshot()[1].ToolCalls[0].Arguments != `{"body":"["text"]"}` {
		t.Fatal("request did not reach provider or canonical arguments changed")
	}
}

func TestRequestExtensionRecoveryHonorsRequiredAndExplicitBlocks(t *testing.T) {
	for _, point := range []extension.InterceptorPoint{extension.PointContextPrepare, extension.PointProviderRequest} {
		for _, mode := range []string{"optional", "required", "block"} {
			t.Run(string(point)+"/"+mode, func(t *testing.T) {
				client := &fakeDispatchClient{interceptFn: func(ev protocol.InterceptEvent, raw json.RawMessage) (protocol.InterceptResult, error) {
					if mode == "block" {
						return blockWith("policy refused"), nil
					}
					var invalid []protocol.ProviderMessage
					if err := json.Unmarshal([]byte(`[{"role":"assistant","tool_calls":[{"id":"a","name":"read","arguments":"[]"}]}]`), &invalid); err != nil {
						t.Fatal(err)
					}
					if point == extension.PointContextPrepare {
						return replaceWith(t, dispatch.ContextPayload{Messages: invalid}), nil
					}
					var payload dispatch.ProviderRequestPayload
					if err := json.Unmarshal(raw, &payload); err != nil {
						t.Fatal(err)
					}
					payload.Request.Messages = invalid
					return replaceWith(t, payload), nil
				}}
				warnings := 0
				d := newExtDispatcher(client, mode == "required", func(string) { warnings++ }, point)
				a := New(&mockProvider{name: "fixture"}, tool.NewRegistry(), NewSession("system"), Options{Extensions: d}, event.Discard)
				got, err := a.prepareSamplingRequest(t.Context())
				if mode != "optional" {
					var summary *SummaryError
					if err == nil || errors.As(err, &summary) {
						t.Fatalf("required extension or explicit block bypassed or reclassified: %v", err)
					}
					return
				}
				if err != nil || warnings != 1 || len(got.req.Messages) != 1 || got.req.Messages[0].Content != "system" {
					t.Fatalf("optional invalid replacement did not preserve original request: %+v %v, warnings=%d", got, err, warnings)
				}
			})
		}
	}
}
