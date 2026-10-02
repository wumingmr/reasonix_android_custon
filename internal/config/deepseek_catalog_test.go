package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"reasonix/internal/provider"
)

func TestDeepSeekCurrentCatalogSurvivesSaveAndReload(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	for _, id := range []string{"deepseek-chat", "deepseek-anthropic", "deepseek-responses"} {
		t.Run(id, func(t *testing.T) {
			preset, _ := CuratedProviderPreset(id)
			cfg := &Config{Providers: preset.Entries, DefaultModel: id + "/deepseek-flash"}
			path := filepath.Join(t.TempDir(), "config.toml")
			for range 2 {
				if err := cfg.SaveTo(path); err != nil {
					t.Fatal(err)
				}
				cfg = LoadForEditWithoutCredentials(path)
				p, ok := cfg.Provider(id)
				if !ok || !reflect.DeepEqual(p.ModelList(), []string{"deepseek-flash", "deepseek-v4-pro"}) || p.DefaultModel() != "deepseek-flash" {
					t.Fatalf("saved preset catalog changed: %+v", p)
				}
				for _, model := range []string{"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
					resolved, ok := cfg.ResolveModel(id + "/" + model)
					if !ok || resolved.Model != model || !EffectiveVision(resolved) {
						t.Fatalf("model reference %q lost wire identity or vision: %+v", model, resolved)
					}
				}
			}
		})
	}
}

func TestDeepSeekCuratedCatalogSurvivesRuntimeAndEditLoad(t *testing.T) {
	for _, models := range []string{`["deepseek-flash"]`, `["deepseek-v4-flash"]`, `["deepseek-v4-flash", "deepseek-v4-pro"]`, `["deepseek-flash", "Custom-Case-ID"]`} {
		t.Run(models, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("REASONIX_HOME", home)
			path := filepath.Join(home, "config.toml")
			raw := "config_version = 10\n[[providers]]\nname = \"deepseek-responses\"\nkind = \"responses\"\nbase_url = \"https://api.deepseek.com\"\nmodels = " + models + "\nvision_models = []\nfuture_field = \"preserve\"\n"
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			var original Config
			if _, err := decodeTOMLFile(path, &original); err != nil {
				t.Fatal(err)
			}
			runtime, err := LoadForRootReadOnly(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for _, cfg := range []*Config{runtime, LoadForEditWithoutCredentials(path)} {
				p, _ := cfg.Provider("deepseek-responses")
				if !reflect.DeepEqual(p.ModelList(), original.Providers[0].ModelList()) || p.VisionModels == nil || len(p.VisionModels) != 0 {
					t.Fatalf("loader overwrote a curated catalog or explicit vision opt-out: %+v", p)
				}
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != raw {
				t.Fatal("loading changed the user's file or unknown fields")
			}
		})
	}
}

func TestDeepSeekLegacyReferenceCompatibilityIsScoped(t *testing.T) {
	for _, tc := range []struct {
		name, kind, base, request string
		models                    []string
		want                      bool
	}{
		{"chat", "openai", "https://api.deepseek.com/v1", "", []string{"deepseek-flash"}, true},
		{"exact override", "openai", "https://api.deepseek.com", "https://api.deepseek.com/v1/chat/completions", []string{"deepseek-flash"}, true},
		{"responses", "responses", "https://api.deepseek.com", "", []string{"deepseek-flash"}, true},
		{"anthropic", "anthropic", "https://api.deepseek.com/anthropic", "", []string{"deepseek-flash"}, true},
		{"third party", "openai", "https://gateway.example/v1", "", []string{"deepseek-flash"}, false},
		{"custom route", "openai", "https://api.deepseek.com/custom", "", []string{"deepseek-flash"}, false},
		{"custom override", "openai", "https://api.deepseek.com", "https://gateway.example/chat/completions", []string{"deepseek-flash"}, false},
		{"Flash removed", "openai", "https://api.deepseek.com", "", []string{"deepseek-v4-pro"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const old = "deepseek-v4-flash"
			price := &provider.Pricing{Input: 5, Output: 10, Currency: "$"}
			cfg := &Config{Providers: []ProviderEntry{{Name: "test", Kind: tc.kind, BaseURL: tc.base, RequestURL: tc.request, Models: tc.models,
				Prices: map[string]*provider.Pricing{old: price}, ModelOverrides: map[string]ProviderModelOverride{old: {SupportedEfforts: []string{"high"}, DefaultEffort: "high"}},
			}}}
			for _, ref := range []string{"test/" + old, old} {
				got, ok := cfg.ResolveModel(ref)
				if ok != tc.want {
					t.Fatalf("ResolveModel(%q) = %v, want %v", ref, ok, tc.want)
				}
				if ok && (got.Model != old || !reflect.DeepEqual(got.Price, price) || !reflect.DeepEqual(got.SupportedEfforts, []string{"high"})) {
					t.Fatalf("legacy reference lost its model-specific configuration: %+v", got)
				}
			}
			for _, unknown := range []string{"DeepSeek-V4-Flash", "deepseek-v4.1-flash-expires-on-0910", "deepseek-future"} {
				if _, ok := cfg.ResolveModel("test/" + unknown); ok {
					t.Fatalf("invented compatibility for %q", unknown)
				}
			}
		})
	}
}

func TestDeepSeekEmptyEntriesUseCurrentCatalog(t *testing.T) {
	for _, id := range []string{"deepseek-chat", "deepseek-anthropic", "deepseek-responses"} {
		preset, _ := CuratedProviderPreset(id)
		entry := preset.Entries[0]
		entry.Models, entry.Model, entry.Default = nil, "", ""
		cfg := &Config{Providers: []ProviderEntry{entry}}
		normalizeOfficialDeepSeekModels(cfg)
		if p := &cfg.Providers[0]; !reflect.DeepEqual(p.ModelList(), []string{"deepseek-flash", "deepseek-v4-pro"}) || p.DefaultModel() != "deepseek-flash" {
			t.Fatalf("%s empty entry uses stale defaults: %+v", id, p)
		}
	}
	cfg := Default()
	if p, ok := cfg.ResolveModel(cfg.DefaultModel); !ok || p.Model != "deepseek-flash" {
		t.Fatalf("new CLI default = %+v", p)
	}
	cfg.Desktop.ProviderAccess = []string{"deepseek"}
	normalizeDesktopOfficialProviderAccess(cfg)
	normalizeOfficialDeepSeekModels(cfg)
	if p, ok := cfg.Provider("deepseek"); !ok || !reflect.DeepEqual(p.ModelList(), []string{"deepseek-flash", "deepseek-v4-pro"}) {
		t.Fatalf("new desktop catalog reintroduced retired aliases: %+v", p)
	}
}
