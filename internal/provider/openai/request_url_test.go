package openai

import (
	"testing"

	"reasonix/internal/provider"
)

func TestNewPrefersExactRequestURLOverLegacyChatURL(t *testing.T) {
	p, err := New(provider.Config{
		BaseURL: "https://base.example.com/v1",
		Model:   "model-a",
		Extra: map[string]any{
			"chat_url":    "https://legacy.example.com/chat/completions/",
			"request_url": "https://exact.example.com/custom/?token=1",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.(*client).chatURL; got != "https://exact.example.com/custom/?token=1" {
		t.Fatalf("chatURL = %q, want exact request_url", got)
	}
}

func TestRequestURLEqualToBaseURLIsIgnored(t *testing.T) {
	p, err := New(provider.Config{
		BaseURL: "https://base.example.com/v1",
		Model:   "model-a",
		Extra:   map[string]any{"request_url": "https://base.example.com/v1"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.(*client).chatURL; got != "https://base.example.com/v1/chat/completions" {
		t.Fatalf("chatURL = %q, want base-derived chat/completions endpoint", got)
	}
}

func TestEndpointOverrideRepeatingBaseResolvesCanonicalChatURL(t *testing.T) {
	const base = "http://localhost:8000/v1"
	for name, extra := range map[string]map[string]any{
		"request_url":            {"request_url": base},
		"request_url slash":      {"request_url": base + "/"},
		"request_url upper host": {"request_url": "HTTP://LOCALHOST:8000/v1"},
		"chat_url":               {"chat_url": base},
		"chat_url upper host":    {"chat_url": "http://LocalHost:8000/v1/"},
		"both repeat base":       {"request_url": base, "chat_url": base},
	} {
		t.Run(name, func(t *testing.T) {
			if got := resolveOpenAIChatURL(base, extra); got != base+"/chat/completions" {
				t.Fatalf("chat URL = %q, want %q", got, base+"/chat/completions")
			}
		})
	}
}

func TestEndpointOverrideDistinctFromBaseStaysVerbatim(t *testing.T) {
	const base = "http://127.0.0.1:8000/v1"
	for _, override := range []string{
		base + "?token=1",
		base + "#debug",
		"http://127.0.0.1:8000/V1",
		"https://127.0.0.1:8000/v1",
		"http://127.0.0.1:8000/custom/chat/completions",
	} {
		if got := resolveOpenAIChatURL(base, map[string]any{"request_url": override}); got != override {
			t.Errorf("request_url %q resolved to %q, want it verbatim", override, got)
		}
		if got := resolveOpenAIChatURL(base, map[string]any{"chat_url": override}); got != override {
			t.Errorf("chat_url %q resolved to %q, want it verbatim", override, got)
		}
	}
}
