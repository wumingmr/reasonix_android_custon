package main

import (
	"reflect"
	"testing"

	"reasonix/internal/config"
)

func TestOfficialDeepSeekTemplateUsesCurrentCatalog(t *testing.T) {
	entries, _, err := officialProviderTemplate("deepseek", "en")
	if err != nil {
		t.Fatal(err)
	}
	p := entries[0]
	if !reflect.DeepEqual(p.ModelList(), []string{"deepseek-flash", "deepseek-v4-pro"}) || p.DefaultModel() != "deepseek-flash" {
		t.Fatalf("official template uses stale models: %+v", p)
	}
	cfg := &config.Config{Providers: entries}
	for _, model := range []string{"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
		e, ok := cfg.ResolveModel("deepseek/" + model)
		if !ok || e.Model != model || !config.EffectiveVision(e) {
			t.Fatalf("official model reference %q lost identity or vision: %+v", model, e)
		}
		if _, err := config.NormalizeEffort(e, "low"); err != nil {
			t.Fatalf("official model reference %q lost low effort: %v", model, err)
		}
	}
}
