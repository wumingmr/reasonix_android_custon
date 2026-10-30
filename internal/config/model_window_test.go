package config

import "testing"

// A relay added by hand records no window anywhere, and a zero window turns
// compaction off. When the model itself is a known one, its own ceiling is the
// answer, so the session is not left growing until the provider refuses it.
func TestARelayInheritsTheWindowKnownForTheModel(t *testing.T) {
	cfg := &Config{Providers: []ProviderEntry{{
		Name: "relay", Kind: "openai", BaseURL: "https://relay.example.com/v1",
		Models: []string{"deepseek-v4-pro"}, Default: "deepseek-v4-pro",
	}}}
	entry, ok := cfg.ResolveModel("relay/deepseek-v4-pro")
	if !ok {
		t.Fatal("relay model did not resolve")
	}
	if entry.ContextWindow != 0 {
		t.Fatalf("entry carries a window %d nobody declared", entry.ContextWindow)
	}
	window, declared := ResolvedContextWindow(entry)
	if !declared || window != 1_000_000 {
		t.Fatalf("window = %d declared=%v, want the model's own ceiling", window, declared)
	}
}

// Everything the configuration says outranks the built-in fact: the catalog is
// the fallback for what nobody declared, not an override of what somebody did.
func TestDeclaredWindowsOutrankTheModelCatalog(t *testing.T) {
	resource := ProviderEntry{
		Name: "relay", Kind: "openai", BaseURL: "https://relay.example.com/v1",
		Models: []string{"deepseek-v4-pro"}, Default: "deepseek-v4-pro",
		ContextWindow: 128_000,
	}
	cfg := &Config{Providers: []ProviderEntry{resource}}
	entry, _ := cfg.ResolveModel("relay/deepseek-v4-pro")
	if window, _ := ResolvedContextWindow(entry); window != 128_000 {
		t.Fatalf("provider-wide window = %d, want the configured 128000", window)
	}

	cfg.Providers[0].ContextWindow = 0
	cfg.Providers[0].ModelOverrides = map[string]ProviderModelOverride{
		"deepseek-v4-pro": {ContextWindow: 64_000},
	}
	entry, _ = cfg.ResolveModel("relay/deepseek-v4-pro")
	if window, _ := ResolvedContextWindow(entry); window != 64_000 {
		t.Fatalf("per-model window = %d, want the configured 64000", window)
	}
}

// Nothing established is not the same as "no compaction wanted". The fallback
// keeps maintenance on for a relay serving a model nobody published numbers
// for, while ResolvedContextWindow still reports that nobody said.
func TestAnUnestablishedWindowFallsBackWithoutClaimingToKnow(t *testing.T) {
	entry := &ProviderEntry{
		Name: "relay", Kind: "openai", BaseURL: "https://relay.example.com/v1",
		Model: "some-unlisted-model",
	}
	if window, declared := ResolvedContextWindow(entry); declared || window != 0 {
		t.Fatalf("unestablished window reported as %d declared=%v", window, declared)
	}
	if window := EffectiveContextWindow(entry); window != DefaultUnknownContextWindow {
		t.Fatalf("effective window = %d, want the %d fallback", window, DefaultUnknownContextWindow)
	}
}

// The per-model override wins even on a raw entry that never went through
// model resolution, which is how a settings surface reads a saved connection.
func TestDeclaredContextWindowReadsTheOverrideDirectly(t *testing.T) {
	entry := &ProviderEntry{
		Name: "relay", Kind: "openai",
		Model:         "deepseek-v4-pro",
		ContextWindow: 200_000,
		ModelOverrides: map[string]ProviderModelOverride{
			"deepseek-v4-pro": {ContextWindow: 64_000},
		},
	}
	if window := entry.declaredContextWindow(); window != 64_000 {
		t.Fatalf("declared window = %d, want the per-model 64000", window)
	}
}
