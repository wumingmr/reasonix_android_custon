package boot

import (
	"errors"
	"reflect"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/provider"
)

func TestLegacyTokenRhythmReasoningAgreesAcrossConsumers(t *testing.T) {
	e := config.ProviderEntry{Name: "token-rhythm", Kind: "openai", BaseURL: "https://tokenrhythm.studio/v1", Model: "deepseek-flash"}
	cfg := &config.Config{DefaultModel: "token-rhythm/deepseek-flash", Providers: []config.ProviderEntry{e}}
	high := "high"
	if err := ValidateReasoningSnapshot(cfg, Options{EffortOverride: &high}); err != nil {
		t.Fatal(err)
	}
	e.Effort = high
	p, err := NewProvider(&e)
	if err != nil {
		t.Fatal(err)
	}
	actual := p.(provider.ReasoningProvider).ReasoningCapability()
	if !reflect.DeepEqual(actual, config.ReasoningCapabilityForEntry(&e)) {
		t.Fatalf("adapter/UI disagree: %+v", actual)
	}
	if !reflect.DeepEqual(actual.IDs(), []string{"disabled", "low", "high", "max"}) {
		t.Fatal(actual)
	}
	if len(cfg.Providers[0].ModelOverrides) != 0 || cfg.Providers[0].Effort != "" {
		t.Fatal("preflight mutated stored config")
	}
}

func TestReasoningPreflightClearsOnlyCrossModelInheritance(t *testing.T) {
	cfg := &config.Config{Providers: []config.ProviderEntry{{Name: "gateway", Kind: "openai", BaseURL: "https://unknown.invalid/v1", Models: []string{"a", "b"}}}}
	high := "high"
	for _, origin := range []string{"gateway/a", "gateway/b", ""} {
		err := ValidateReasoningSnapshot(cfg, Options{Model: "gateway/b", EffortModel: origin, EffortOverride: &high})
		if origin == "gateway/a" {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			var diagnostic *provider.UnsupportedReasoningEffort
			if !errors.As(err, &diagnostic) || !diagnostic.Unknown {
				t.Fatalf("explicit invalid choice was hidden: %v", err)
			}
		}
	}
}
