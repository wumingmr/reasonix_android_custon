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
	window, source := entry.declaredContextWindow()
	if window != 64_000 || source != ContextWindowFromModelOverride {
		t.Fatalf("declared window = %d from %s, want the per-model 64000", window, source)
	}
}

// The origin has to be named, because a window somebody chose and one nothing
// established need opposite fixes. Most specific source wins.
func TestResolveContextWindowNamesTheOrigin(t *testing.T) {
	relay := ProviderEntry{
		Name: "relay", Kind: "openai", BaseURL: "https://relay.example/v1",
		Model: "deepseek-v4-pro",
	}
	if got := ResolveContextWindow(&relay); got.Tokens != 1_000_000 || got.Source != ContextWindowFromModelCatalog {
		t.Fatalf("catalog window = %+v", got)
	}

	relay.ContextWindow = 200_000
	if got := ResolveContextWindow(&relay); got.Tokens != 200_000 || got.Source != ContextWindowFromProvider {
		t.Fatalf("provider window = %+v", got)
	}

	relay.ModelOverrides = map[string]ProviderModelOverride{"deepseek-v4-pro": {ContextWindow: 64_000}}
	if got := ResolveContextWindow(&relay); got.Tokens != 64_000 || got.Source != ContextWindowFromModelOverride {
		t.Fatalf("override window = %+v", got)
	}

	unknown := ProviderEntry{Name: "relay", Kind: "openai", Model: "some-unlisted-model"}
	if got := ResolveContextWindow(&unknown); got.Tokens != 0 || got.Established() {
		t.Fatalf("unestablished window = %+v", got)
	}
	if got := EffectiveResolvedWindow(&unknown); got.Tokens != DefaultUnknownContextWindow || got.Source != ContextWindowFromFallback {
		t.Fatalf("fallback window = %+v", got)
	}
}

// The fallback is a value nobody established, so it must never claim to be one:
// a surface that showed it as the model's ceiling would send the user looking
// for a limit that was never stated.
func TestTheFallbackNeverClaimsToBeEstablished(t *testing.T) {
	if ContextWindowFromFallback.IsEstablished() {
		t.Fatal("the fallback reports itself as established")
	}
	for _, source := range []ContextWindowSource{ContextWindowFromModelOverride, ContextWindowFromProvider, ContextWindowFromModelCatalog} {
		if !source.IsEstablished() {
			t.Fatalf("%s should count as established", source)
		}
	}
	if ContextWindowUnestablished.IsEstablished() {
		t.Fatal("nothing-established reports itself as established")
	}
}

// The tokens are stable because they also appear in machine-read output.
func TestContextWindowSourceTokens(t *testing.T) {
	for source, want := range map[ContextWindowSource]string{
		ContextWindowUnestablished:     "unestablished",
		ContextWindowFromModelOverride: "model_override",
		ContextWindowFromProvider:      "provider",
		ContextWindowFromModelCatalog:  "model_catalog",
		ContextWindowFromFallback:      "fallback",
	} {
		if got := source.String(); got != want {
			t.Errorf("%d = %q, want %q", source, got, want)
		}
	}
}
