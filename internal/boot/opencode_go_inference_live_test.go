//go:build live

package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptrace"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/provider"
)

// Opt-in, synthetic coding traffic only: eight logical inference requests,
// each capped at 768 output tokens/60 seconds, plus shared-model discovery.
// No credentials or generated text are logged.
func TestLiveOpenCodeGoInferenceV41(t *testing.T) {
	runLiveOpenCodeGoInferenceV41(t, false)
}

// A focused rerun keeps one conversation in memory through three dependent
// requests, with at most five explicit 503-only probe retries shared across it.
// This is test orchestration, not a change to the production retry policy.
func TestLiveOpenCodeGoInferenceV41ToolReplay(t *testing.T) {
	runLiveOpenCodeGoInferenceV41(t, true)
}

func runLiveOpenCodeGoInferenceV41(t *testing.T, replayOnly bool) {
	key := os.Getenv("OPENCODE_GO_API_KEY")
	if key == "" {
		t.Skip("OPENCODE_GO_API_KEY is required")
	}
	t.Setenv("REASONIX_HOME", t.TempDir())
	requests, input, output, cache := 0, 0, 0, 0
	t.Cleanup(func() {
		t.Logf("requests=%d input_tokens=%d output_tokens=%d cached_input_tokens=%d", requests, input, output, cache)
	})
	newModel := func(kind, base, model string) provider.Provider {
		e := config.ProviderEntry{Name: "opencode-go-live", Kind: kind, BaseURL: base, Model: model, APIKeyEnv: "OPENCODE_GO_API_KEY", MaxOutputTokens: 768, ResponsesMode: "stateless"}
		e.ResolveAPIKeyFromProcessEnvForProbe()
		p, err := NewProvider(&e)
		if err != nil {
			t.Fatal(strings.ReplaceAll(err.Error(), key, "[redacted]"))
		}
		if kind == "openai" {
			resolved := config.ResolveReasoningEntry(&e)
			if config.ReasoningProtocolForEntry(resolved) != "deepseek" || config.ReasoningCapabilityForEntry(resolved).Default != "high" || !provider.RequiresToolCallReasoning(p) {
				t.Fatal("boot factory lost the automatic DeepSeek reasoning/replay contract")
			}
		}
		if closer, ok := p.(interface{ CloseIdleConnections() }); ok {
			t.Cleanup(closer.CloseIdleConnections)
		}
		return p
	}
	sessions := map[string]string{}
	probeSession := provider.NewCacheSessionID()
	probeRetries := 0
	if replayOnly {
		probeRetries = 5
	}
	var collect func(string, provider.Provider, provider.Request) provider.Message
	collect = func(label string, p provider.Provider, req provider.Request) provider.Message {
		t.Helper()
		requests++
		if requests > 8 {
			t.Fatal("logical request budget exceeded")
		}
		req.MaxTokens = 768
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		conversation := label
		if strings.HasPrefix(label, "new-default-") {
			conversation = "default-tool-history"
		}
		identity := probeSession + "/" + conversation
		// An explicit synthetic identity permits an affinity-control probe.
		// Never use a real user's conversation identity in this live fixture.
		if pinned := os.Getenv("REASONIX_OPENCODE_LIVE_SESSION"); pinned != "" {
			identity = pinned
		}
		ctx = provider.WithIndependentRequestAttemptCounter(provider.WithCacheSession(ctx, identity))
		var mu sync.Mutex
		var status int
		var path, session, userAgent string
		ctx = provider.WithRequestObserver(ctx, func(o provider.RequestObservation) {
			mu.Lock()
			defer mu.Unlock()
			if o.Status != 0 {
				status = o.Status
			}
			if o.RequestPath != "" {
				path = o.RequestPath
			}
		})
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteHeaderField: func(name string, values []string) {
			mu.Lock()
			defer mu.Unlock()
			if len(values) == 0 {
				return
			}
			switch strings.ToLower(name) {
			case "x-opencode-session":
				session = values[0]
			case "user-agent":
				userAgent = values[0]
			}
		}})
		start := time.Now()
		ch, err := p.Stream(ctx, req)
		message := provider.Message{Role: provider.RoleAssistant}
		var usage provider.Usage
		if err == nil {
			for chunk := range ch {
				switch chunk.Type {
				case provider.ChunkText:
					message.Content += chunk.Text
				case provider.ChunkReasoning:
					message.ReasoningContent += chunk.Text
				case provider.ChunkToolCall:
					if chunk.ToolCall != nil {
						message.ToolCalls = append(message.ToolCalls, *chunk.ToolCall)
					}
				case provider.ChunkUsage:
					if chunk.Usage != nil {
						usage = *chunk.Usage
					}
				case provider.ChunkError:
					if chunk.Err != nil {
						err = chunk.Err
					}
				}
			}
		}
		mu.Lock()
		gotStatus, gotPath, gotSession, gotAgent := status, path, session, userAgent
		mu.Unlock()
		input += usage.PromptTokens
		output += usage.CompletionTokens
		cache += usage.CacheHitTokens
		t.Logf("case=%s status=%d path=%s elapsed=%s attempts=%d input=%d output=%d cache=%d text_bytes=%d reasoning_bytes=%d tools=%d session_header=%t client_header=%t", label, gotStatus, gotPath, time.Since(start).Round(time.Millisecond), provider.RequestAttemptCount(ctx), usage.PromptTokens, usage.CompletionTokens, usage.CacheHitTokens, len(message.Content), len(message.ReasoningContent), len(message.ToolCalls), gotSession != "", gotAgent != "")
		if err != nil {
			detail := err.Error()
			var api *provider.APIError
			if errors.As(err, &api) {
				detail = fmt.Sprintf("HTTP %d: %s", api.Status, api.Body)
			}
			detail = strings.ReplaceAll(detail, key, "[redacted]")
			if len(detail) > 1024 {
				detail = detail[:1024]
			}
			if replayOnly && gotStatus == 503 && probeRetries > 0 {
				probeRetries--
				t.Logf("explicit_probe_retry case=%s remaining=%d delay=10s error=%s", label, probeRetries, detail)
				timer := time.NewTimer(10 * time.Second)
				defer timer.Stop()
				select {
				case <-t.Context().Done():
					t.Fatal(t.Context().Err())
				case <-timer.C:
				}
				return collect(label, p, req)
			}
			if gotStatus == 401 || gotStatus == 403 {
				t.Fatal(detail)
			}
			t.Error(detail)
			return message
		}
		if gotStatus != 200 || gotSession == "" || gotAgent != "Reasonix" {
			t.Fatal("missing successful HTTP response or Go client/session identity")
		}
		if old := sessions[conversation]; old != "" && old != gotSession {
			t.Fatal("conversation session header changed")
		}
		sessions[conversation] = gotSession
		if usage.PromptTokens == 0 || usage.CompletionTokens == 0 {
			t.Fatal("missing usage accounting")
		}
		if message.Content == "" && len(message.ToolCalls) == 0 {
			t.Fatal("no deliverable response within output cap")
		}
		return message
	}
	chat := newModel("openai", provider.OpenCodeGoChatBaseURL, "deepseek-v4.1-flash")
	smoke := provider.Request{Messages: []provider.Message{{Role: provider.RoleSystem, Content: "You are Reasonix, a coding assistant running a small integration test. Follow instructions briefly."}, {Role: provider.RoleUser, Content: "Reply with only OK."}}, EffortOverride: "disabled"}
	if !replayOnly {
		collect("new-disabled", chat, smoke)
	}
	tools := []provider.ToolSchema{{Name: "get_marker", Description: "Return the synthetic coding integration marker. Call exactly once when requested.", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}
	messages := []provider.Message{{Role: provider.RoleSystem, Content: "You are Reasonix, a coding assistant. Call get_marker exactly once when asked. After a tool result, reply with only the returned marker. Never call it again unless asked."}, {Role: provider.RoleUser, Content: "Call get_marker now."}}
	first := collect("new-default-tool", chat, provider.Request{Messages: messages, Tools: tools})
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "get_marker" {
		t.Error("missing requested tool call")
	} else {
		messages = append(messages, first, provider.Message{Role: provider.RoleTool, ToolCallID: first.ToolCalls[0].ID, Name: "get_marker", Content: "reasonix-v41-protocol-ok"})
		second := collect("new-default-tool-result", chat, provider.Request{Messages: messages, Tools: tools})
		if !strings.Contains(second.Content, "reasonix-v41-protocol-ok") || len(second.ToolCalls) != 0 {
			t.Error("tool replay did not preserve result")
		} else {
			messages = append(messages, second, provider.Message{Role: provider.RoleUser, Content: "Repeat only the previous marker. Do not call any tool."})
			third := collect("new-default-multiturn", chat, provider.Request{Messages: messages, Tools: tools})
			if !strings.Contains(third.Content, "reasonix-v41-protocol-ok") || len(third.ToolCalls) != 0 {
				t.Error("multi-turn reasoning/history replay failed")
			}
		}
	}
	if replayOnly {
		return
	}
	for _, effort := range []string{"low", "max"} {
		smoke.EffortOverride = effort
		collect("new-"+effort, chat, smoke)
	}
	legacy := newModel("openai", "https://opencode.ai/zen/go/v1", "deepseek-v4.1-flash")
	smoke.EffortOverride = "disabled"
	collect("legacy-disabled", legacy, smoke)
	// The second documented protocol family shares the same migrated Go key.
	anthropic := newModel("anthropic", provider.OpenCodeGoAnthropicBaseURL, "qwen3.7-plus")
	smoke.EffortOverride = ""
	collect("new-anthropic", anthropic, smoke)
	entry := config.ProviderEntry{Name: "opencode-go-live", Kind: "openai", BaseURL: provider.OpenCodeGoChatBaseURL, APIKeyEnv: "OPENCODE_GO_API_KEY"}
	entry.ResolveAPIKeyFromProcessEnvForProbe()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	models, err := entry.FetchModelCatalog(ctx)
	if err != nil {
		t.Error(strings.ReplaceAll(err.Error(), key, "[redacted]"))
		return
	}
	found := false
	for _, model := range models {
		if model.ID == "deepseek-v4.1-flash" {
			found = true
			t.Logf("discovery_v41=true image_input=%t context=%d max_output=%d", model.SupportsInput(provider.ModalityImage), model.ContextWindow, model.MaxOutputTokens)
		}
	}
	t.Logf("discovered_chat_models=%d", len(models))
	if !found {
		t.Error("V4.1 Flash missing from shared-model discovery")
	}
}
