package config

import (
	"testing"

	"reasonix/internal/provider/openai"
)

func TestEffectiveVisionEnablesPinnedOfficialDeepSeekVisionSKU(t *testing.T) {
	sku := &ProviderEntry{
		Name:    "deepseek",
		Kind:    "openai",
		BaseURL: "https://api.deepseek.com",
		Model:   openai.OfficialDeepSeekVisionModel,
	}
	if !CanConfigureVision(sku) {
		t.Fatal("official DeepSeek Settings must expose per-model image-input checkboxes")
	}
	if !EffectiveVision(sku) || !ExplicitModelVision(sku) {
		t.Fatal("selecting the pinned official DeepSeek vision SKU must enable image input")
	}
}

func TestEffectiveVisionHonorsOfficialDeepSeekVisionModels(t *testing.T) {
	sku := &ProviderEntry{
		Name:         "deepseek",
		Kind:         "openai",
		BaseURL:      "https://api.deepseek.com",
		Model:        openai.OfficialDeepSeekVisionModel,
		VisionModels: []string{openai.OfficialDeepSeekVisionModel},
	}
	if !EffectiveVision(sku) || !ExplicitModelVision(sku) {
		t.Fatal("checking image input on the official vision SKU must enable image input")
	}

	sku.VisionModels = []string{}
	if EffectiveVision(sku) || ExplicitModelVision(sku) {
		t.Fatal("unchecking image input must disable image input on the official vision SKU")
	}

	pro := &ProviderEntry{
		Name:         "deepseek",
		Kind:         "openai",
		BaseURL:      "https://api.deepseek.com",
		Model:        "deepseek-v4-pro",
		VisionModels: []string{"deepseek-v4-pro", openai.OfficialDeepSeekVisionModel},
	}
	if EffectiveVision(pro) || ExplicitModelVision(pro) {
		t.Fatal("checking image input on a text-only model must not enable official DeepSeek image payloads")
	}

	// V4.1 Flash is natively multimodal, so a curated list that predates it must
	// not veto it — while an explicitly emptied list still turns images off.
	flash := &ProviderEntry{
		Name:         "deepseek",
		Kind:         "openai",
		BaseURL:      "https://api.deepseek.com",
		Model:        "deepseek-flash",
		VisionModels: []string{openai.OfficialDeepSeekVisionModel},
	}
	if !EffectiveVision(flash) {
		t.Fatal("a stale curated vision list vetoed a natively multimodal model")
	}
	flash.VisionModels = []string{}
	if EffectiveVision(flash) {
		t.Fatal("an explicitly emptied vision list must disable image input")
	}
}

func TestNormalizeOfficialDeepSeekModelsPreservesLegacyCatalogs(t *testing.T) {
	for _, name := range []string{"deepseek", "deepseek-flash", "deepseek-chat", "deepseek-anthropic", "deepseek-responses"} {
		for _, models := range [][]string{{"deepseek-v4-flash"}, {"deepseek-v4-flash", "deepseek-v4-pro"}} {
			c := &Config{Providers: []ProviderEntry{{Name: name, Kind: "openai", BaseURL: "https://api.deepseek.com", Models: append([]string(nil), models...), Default: models[0]}}}
			normalizeOfficialDeepSeekModels(c)
			p := &c.Providers[0]
			if !stringSlicesEqual(p.ModelList(), models) || p.Default != models[0] || p.VisionModels != nil {
				t.Fatalf("%s normalization changed legacy selections: %+v", name, p)
			}
		}
	}
}

func TestNormalizeOfficialDeepSeekModelsSkipsVisionOnProProvider(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name:      "deepseek-pro",
		Kind:      "openai",
		BaseURL:   "https://api.deepseek.com",
		Models:    []string{"deepseek-v4-pro"},
		Default:   "deepseek-v4-pro",
		APIKeyEnv: "DEEPSEEK_API_KEY",
	}}}
	normalizeOfficialDeepSeekModels(c)
	p, ok := c.Provider("deepseek-pro")
	if !ok {
		t.Fatal("deepseek-pro provider missing")
	}
	if p.HasModel(openai.OfficialDeepSeekVisionModel) {
		t.Fatalf("deepseek-pro models = %v, want pro only", p.ModelList())
	}
}

func TestNormalizeOfficialDeepSeekModelsPreservesExplicitEmptyVisionModels(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name:         "deepseek",
		Kind:         "openai",
		BaseURL:      "https://api.deepseek.com",
		Models:       []string{"deepseek-v4-flash", "deepseek-v4-pro"},
		Default:      "deepseek-v4-flash",
		VisionModels: []string{},
		APIKeyEnv:    "DEEPSEEK_API_KEY",
	}}}
	normalizeOfficialDeepSeekModels(c)
	p, ok := c.Provider("deepseek")
	if !ok {
		t.Fatal("deepseek provider missing")
	}
	if p.HasModel(openai.OfficialDeepSeekVisionModel) {
		t.Fatal("explicit empty vision_models must not re-add the vision SKU to a Flash/Pro catalog")
	}
	if p.VisionModels == nil || len(p.VisionModels) != 0 {
		t.Fatalf("vision_models = %#v, want preserved explicit empty list", p.VisionModels)
	}
}

func TestDeepSeekV4PricesIncludeVisionSKU(t *testing.T) {
	for _, currency := range []string{"CNY", "USD"} {
		prices := DeepSeekV4PricesForCurrency(currency)
		flash := prices["deepseek-v4-flash"]
		got := prices[openai.OfficialDeepSeekVisionModel]
		if flash == nil || got == nil || got.CacheHit != flash.CacheHit || got.Input != flash.Input || got.Output != flash.Output || got.Currency != flash.Currency {
			t.Fatalf("%s vision SKU price = %+v, want Flash table %+v", currency, got, flash)
		}
	}
}

func TestDeepSeekOfficialPresetsRouteVisionToMultimodalSKUs(t *testing.T) {
	for _, id := range []string{"deepseek-chat", "deepseek-anthropic", "deepseek-responses"} {
		preset, ok := CuratedProviderPreset(id)
		if !ok || len(preset.Entries) != 1 {
			t.Fatalf("%s preset = %+v found=%v", id, preset, ok)
		}
		entry := preset.Entries[0]
		if !stringSlicesEqual(entry.ModelList(), []string{"deepseek-flash", "deepseek-v4-pro"}) || entry.Default != "deepseek-flash" || entry.Vision {
			t.Fatalf("%s entry = %+v", id, entry)
		}
		var cfg Config
		if err := cfg.UpsertProvider(entry); err != nil {
			t.Fatalf("UpsertProvider(%s): %v", id, err)
		}
		flash, _ := cfg.ResolveModel(entry.Name + "/deepseek-flash")
		pro, _ := cfg.ResolveModel(entry.Name + "/deepseek-v4-pro")
		vision, ok := cfg.ResolveModel(entry.Name + "/" + openai.OfficialDeepSeekVisionModel)
		if flash == nil || pro == nil || !ok {
			t.Fatalf("%s models did not resolve", id)
		}
		if !EffectiveVision(flash) || EffectiveVision(pro) || !EffectiveVision(vision) {
			t.Fatalf("%s vision routing = flash:%t pro:%t vision:%t", id, EffectiveVision(flash), EffectiveVision(pro), EffectiveVision(vision))
		}
	}
}
