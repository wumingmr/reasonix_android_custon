package config

import (
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

func TestModelEffortIndependentRoundTripAndRuntimePriority(t *testing.T) {
	cfg := &Config{Providers: []ProviderEntry{{Name: "relay", Kind: "openai", BaseURL: "https://relay.test/v1", Models: []string{"a", "b"}, Effort: "high", ReasoningProtocol: "openai", SupportedEfforts: []string{"low", "high"}, DefaultEffort: "low"}}}
	low := "low"
	if err := cfg.SetModelEffort("relay", "a", &low); err != nil {
		t.Fatal(err)
	}
	a, _ := cfg.ResolveModel("relay/a")
	b, _ := cfg.ResolveModel("relay/b")
	if EffectiveEffort(a) != "low" || EffectiveEffort(b) != "high" {
		t.Fatal("model effort leaked to sibling")
	}
	a.SetRuntimeEffort("high")
	if EffectiveEffort(a) != "high" {
		t.Fatal("session effort lost to model setting")
	}
	auto := ""
	if err := cfg.SetModelEffort("relay", "a", &auto); err != nil {
		t.Fatal(err)
	}
	a, _ = cfg.ResolveModel("relay/a")
	if EffectiveEffort(a) != "low" {
		t.Fatal("model auto inherited pinned connection effort")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	loaded := LoadForEditWithoutCredentials(path)
	if loaded.Providers[0].ModelOverrides["a"].Effort == nil {
		t.Fatal("explicit auto lost on save")
	}
	if err := loaded.SetModelEffort("relay", "a", nil); err != nil {
		t.Fatal(err)
	}
	a, _ = loaded.ResolveModel("relay/a")
	if EffectiveEffort(a) != "high" {
		t.Fatal("inherit did not restore connection")
	}
}

func TestDiscoveredReasoningIsScopedAndManualWins(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	e := ProviderEntry{Name: "relay", Kind: "openai", BaseURL: "http://127.0.0.1:8790/v1", Models: []string{"a", "b"}}
	e.ApplyDiscoveredReasoning([]provider.ModelInfo{{ID: "a", ReasoningInfo: &provider.ModelReasoningInfo{Protocol: "openai", Efforts: []string{"low", "high"}, Default: "low"}}})
	cfg := &Config{Providers: []ProviderEntry{e}}
	a, _ := cfg.ResolveModel("relay/a")
	if EffectiveEffort(a) != "low" || ReasoningCapabilityForEntry(a).Unknown {
		t.Fatalf("discovery not active: %+v", a)
	}
	b, _ := cfg.ResolveModel("relay/b")
	if !ReasoningCapabilityForEntry(b).Unknown {
		t.Fatal("discovery applied to sibling")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	loaded := LoadForEditWithoutCredentials(path)
	a, _ = loaded.ResolveModel("relay/a")
	if EffectiveEffort(a) != "low" {
		t.Fatal("discovery did not roundtrip")
	}
	ov := loaded.Providers[0].ModelOverrides["a"]
	ov.ReasoningProtocol = "none"
	loaded.Providers[0].ModelOverrides["a"] = ov
	a, _ = loaded.ResolveModel("relay/a")
	if ReasoningCapabilityForEntry(a).State() != "unsupported" {
		t.Fatal("discovery overwrote manual none")
	}
	loaded.Providers[0].BaseURL = "https://other.test/v1"
	ov.ReasoningProtocol = ""
	loaded.Providers[0].ModelOverrides["a"] = ov
	a, _ = loaded.ResolveModel("relay/a")
	if !ReasoningCapabilityForEntry(a).Unknown {
		t.Fatal("new endpoint inherited old declaration")
	}
}

func TestDiscoveredReasoningExpiresAndCloneIsIndependent(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	e := ProviderEntry{Name: "relay", Kind: "openai", BaseURL: "https://relay.test", Model: "a"}
	e.ApplyDiscoveredReasoning([]provider.ModelInfo{{ID: "a", ReasoningInfo: &provider.ModelReasoningInfo{Protocol: "openai", Efforts: []string{"low", "high"}, Default: "low"}}})
	copy := cloneModelOverrideMap(e.ModelOverrides)
	copy["a"].DiscoveredReasoning.Efforts[0] = "changed"
	if e.ModelOverrides["a"].DiscoveredReasoning.Efforts[0] != "low" {
		t.Fatal("clone shares discovery slice")
	}
	runtime := ResolveReasoningEntry(&e)
	e.ModelOverrides["a"].DiscoveredReasoning.ExpiresAt = 1
	if got := ResolveReasoningEntry(runtime); got.ReasoningProtocol != "" || len(got.SupportedEfforts) != 0 {
		t.Fatal("expired facts survived re-resolution")
	}
}

func TestDiscoveredReasoningNeverInventsLevelsOrBreaksFixedProtocols(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	for _, r := range []*provider.ModelReasoningInfo{{Parameter: true}, {Protocol: "glm", Efforts: []string{"low", "high"}}, {Protocol: "kimi-k3", Efforts: []string{"medium"}}} {
		e := ProviderEntry{Name: "relay", Kind: "openai", BaseURL: "https://relay.test", Model: "custom-model"}
		e.ApplyDiscoveredReasoning([]provider.ModelInfo{{ID: e.Model, ReasoningInfo: r}})
		got := ResolveReasoningEntry(&e)
		if len(got.SupportedEfforts) != 0 || got.ReasoningProtocol != "" {
			t.Fatalf("unsafe declaration applied: %+v", got)
		}
	}
}
