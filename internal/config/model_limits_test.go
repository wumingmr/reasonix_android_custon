package config

import (
	"testing"

	"reasonix/internal/provider"
)

// The stored limits are per model, so setting one never disturbs another
// model's reasoning ladder or vision flag on the same connection.
func TestSetModelLimitsPreservesTheRestOfTheOverride(t *testing.T) {
	vision := true
	entry := &ProviderEntry{
		Name: "relay", Kind: "openai",
		Models: []string{"a", "b"},
		ModelOverrides: map[string]ProviderModelOverride{
			"a": {ReasoningProtocol: "deepseek", SupportedEfforts: []string{"low", "high"}, Vision: &vision},
			"b": {ContextWindow: 32_000},
		},
	}
	if err := entry.SetModelLimits("a", ModelLimits{ContextWindow: 128_000, MaxOutputTokens: 8_192}); err != nil {
		t.Fatal(err)
	}
	kept := entry.ModelOverrides["a"]
	if kept.ReasoningProtocol != "deepseek" || len(kept.SupportedEfforts) != 2 || kept.Vision == nil || !*kept.Vision {
		t.Fatalf("unrelated fields were dropped: %+v", kept)
	}
	if kept.ContextWindow != 128_000 || kept.MaxOutputTokens != 8_192 {
		t.Fatalf("limits not stored: %+v", kept)
	}
	if entry.ModelOverrides["b"].ContextWindow != 32_000 {
		t.Fatal("another model's limits changed")
	}
}

// Clearing both limits removes the override so the model inherits again, and an
// override that still carries other declarations is kept rather than deleted.
func TestSetModelLimitsClearsByInheriting(t *testing.T) {
	entry := &ProviderEntry{
		Name: "relay", Kind: "openai", Models: []string{"a", "b"},
		ModelOverrides: map[string]ProviderModelOverride{
			"a": {ContextWindow: 1_000},
			"b": {ContextWindow: 2_000, ReasoningProtocol: "deepseek"},
		},
	}
	if err := entry.SetModelLimits("a", ModelLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := entry.ModelOverrides["a"]; ok {
		t.Fatal("cleared override survived")
	}
	if err := entry.SetModelLimits("b", ModelLimits{}); err != nil {
		t.Fatal(err)
	}
	if entry.ModelOverrides["b"].ReasoningProtocol != "deepseek" {
		t.Fatal("clearing limits discarded the reasoning declaration")
	}
	if _, ok := entry.ModelLimits("b"); ok {
		t.Fatal("cleared limits still report themselves as declared")
	}
}

// A negative window has nothing to mean, so it is refused instead of silently
// reinterpreted as "inherit".
func TestSetModelLimitsRefusesANegativeWindow(t *testing.T) {
	entry := &ProviderEntry{Name: "relay", Kind: "openai", Models: []string{"a"}}
	if err := entry.SetModelLimits("a", ModelLimits{ContextWindow: -1}); err == nil {
		t.Fatal("negative window accepted")
	}
}

// InheritedLimits is what a blank field on a settings form means, so it must
// report the connection's value with the model's own limits taken back out.
func TestInheritedLimitsDropsOnlyTheModelsOwnLimits(t *testing.T) {
	entry := &ProviderEntry{
		Name: "relay", Kind: "openai", Models: []string{"a"},
		ContextWindow: 200_000, MaxOutputTokens: 4_096,
		ModelOverrides: map[string]ProviderModelOverride{
			"a": {ContextWindow: 64_000, MaxOutputTokens: 1_024},
		},
	}
	if got := entry.InheritedLimits("a"); got.ContextWindow != 200_000 || got.MaxOutputTokens != 4_096 {
		t.Fatalf("inherited = %+v, want the connection's 200000/4096", got)
	}
	if limits, ok := entry.ModelLimits("a"); !ok || limits.ContextWindow != 64_000 {
		t.Fatalf("stored limits = %+v ok=%v", limits, ok)
	}
}

// Discovery fills only gaps: a window the person declared is their answer, and
// a model the listing said nothing about stays inheriting.
func TestApplyDiscoveredLimitsFillsOnlyGaps(t *testing.T) {
	entry := &ProviderEntry{
		Name: "relay", Kind: "openai",
		Models: []string{"known", "declared", "silent"},
		ModelOverrides: map[string]ProviderModelOverride{
			"declared": {ContextWindow: 64_000},
		},
	}
	catalog := []provider.ModelInfo{
		{ID: "known", ContextWindow: 1_000_000, MaxOutputTokens: 131_072},
		{ID: "declared", ContextWindow: 128_000},
		{ID: "silent"},
	}
	if filled := entry.ApplyDiscoveredLimits(catalog); filled != 1 {
		t.Fatalf("filled = %d, want only the model that had no window", filled)
	}
	known, ok := entry.ModelLimits("known")
	if !ok || known.ContextWindow != 1_000_000 || known.MaxOutputTokens != 131_072 {
		t.Fatalf("discovered limits = %+v ok=%v", known, ok)
	}
	if entry.ModelOverrides["declared"].ContextWindow != 64_000 {
		t.Fatal("a declared window was overwritten by the listing")
	}
	if _, ok := entry.ModelLimits("silent"); ok {
		t.Fatal("a model the listing said nothing about gained limits")
	}
}

// A connection-wide window is the person's answer for every model it covers, so
// a listing must not quietly replace it with per-model overrides. Only a model
// that inherits nothing has a real gap for discovery to fill.
func TestApplyDiscoveredLimitsRespectsAConnectionWindow(t *testing.T) {
	entry := &ProviderEntry{
		Name: "relay", Kind: "openai",
		Models:        []string{"m", "n"},
		ContextWindow: 128_000,
	}
	catalog := []provider.ModelInfo{
		{ID: "m", ContextWindow: 1_000_000},
		{ID: "n", ContextWindow: 500_000},
	}
	if filled := entry.ApplyDiscoveredLimits(catalog); filled != 0 {
		t.Fatalf("discovery wrote %d overrides over a connection window: %+v", filled, entry.ModelOverrides)
	}
	if _, ok := entry.ModelLimits("m"); ok {
		t.Fatal("a model gained an override despite the connection-wide window")
	}

	// With no connection window the models do inherit nothing, so the gap is
	// real and must still be filled.
	bare := &ProviderEntry{Name: "relay", Kind: "openai", Models: []string{"m"}}
	if filled := bare.ApplyDiscoveredLimits(catalog); filled != 1 {
		t.Fatalf("a real gap was not filled: %d", filled)
	}
}
