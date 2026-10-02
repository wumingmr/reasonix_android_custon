package config

import "testing"

func TestMimoPresetCapabilities(t *testing.T) {
	var cfg Config
	for _, preset := range CuratedProviderPresets() {
		for _, entry := range preset.Entries {
			if err := cfg.UpsertProvider(entry); err != nil {
				t.Fatalf("upsert preset %q: %v", preset.ID, err)
			}
		}
	}
	mimo, ok := cfg.Provider("mimo-api")
	if !ok {
		t.Fatal("mimo-api provider missing")
	}
	if !mimo.NoProxy {
		t.Fatal("mimo-api preset should bypass configured proxy for China-only endpoint")
	}
	if mimo.DefaultModel() != "mimo-v2.6-pro" || !mimo.HasVisionModel("mimo-v2.6-pro") ||
		!mimo.HasVisionModel("mimo-v2.6-flash") || !mimo.HasVisionModel("mimo-v2.5") || mimo.HasVisionModel("mimo-v2.5-pro") {
		t.Fatalf("mimo vision capability mismatch: %+v", mimo.VisionModels)
	}
	if price := mimo.PriceForModel("mimo-v2.6-flash"); price == nil || price.Currency != "¥" || price.Input != 1 || price.Output != 2 || price.CacheHit != 0.02 {
		t.Fatalf("mimo-v2.6-flash price = %+v, want current RMB pricing", price)
	}
	if price := mimo.PriceForModel("mimo-v2.5-pro"); price == nil || price.Currency != "¥" {
		t.Fatalf("mimo-v2.5-pro price = %+v, want RMB pricing", price)
	}
	mimoAnthropic, ok := cfg.Provider("mimo-anthropic")
	if !ok {
		t.Fatal("mimo-anthropic provider missing")
	}
	if mimoAnthropic.Kind != "anthropic" || mimoAnthropic.BaseURL != "https://api.xiaomimimo.com/anthropic" || mimoAnthropic.Thinking != "adaptive" {
		t.Fatalf("mimo-anthropic capability mismatch: %+v", mimoAnthropic)
	}
	mimoPlan, ok := cfg.Provider("mimo-token-plan-cn")
	if !ok {
		t.Fatal("mimo-token-plan-cn provider missing")
	}
	if !mimoPlan.NoProxy || mimoPlan.APIKeyEnv != "MIMO_TOKEN_PLAN_API_KEY" || !mimoPlan.HasVisionModel("mimo-v2.6-pro") || !mimoPlan.HasVisionModel("mimo-v2.5") {
		t.Fatalf("mimo-token-plan-cn capability mismatch: %+v", mimoPlan)
	}
	mimoSGP, ok := cfg.Provider("mimo-token-plan-sgp")
	if !ok {
		t.Fatal("mimo-token-plan-sgp provider missing")
	}
	if mimoSGP.NoProxy || mimoSGP.BaseURL != "https://token-plan-sgp.xiaomimimo.com/v1" {
		t.Fatalf("mimo-token-plan-sgp endpoint/proxy mismatch: %+v", mimoSGP)
	}
	mimoPlanAnthropic, ok := cfg.Provider("mimo-token-plan-cn-anthropic")
	if !ok {
		t.Fatal("mimo-token-plan-cn-anthropic provider missing")
	}
	if mimoPlanAnthropic.Kind != "anthropic" || !mimoPlanAnthropic.NoProxy || mimoPlanAnthropic.BaseURL != "https://token-plan-cn.xiaomimimo.com/anthropic" {
		t.Fatalf("mimo-token-plan-cn-anthropic capability mismatch: %+v", mimoPlanAnthropic)
	}

}
