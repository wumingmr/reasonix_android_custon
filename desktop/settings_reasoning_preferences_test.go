package main

import (
	"testing"

	"reasonix/internal/config"
)

func TestSubagentPreferenceUsesDisplayedReasoningCapabilities(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("REASONIX_HOME", t.TempDir())
	c := &config.Config{Providers: []config.ProviderEntry{{
		Name: "gateway", Kind: "openai", BaseURL: "https://api.deepseek.com",
		Models: []string{"deepseek-v4-flash", "deepseek-v4-pro"},
		ModelOverrides: map[string]config.ProviderModelOverride{
			"deepseek-v4-pro": {ReasoningProtocol: "none"},
		},
	}}, DefaultModel: "gateway/deepseek-v4-flash"}
	view := providerViewFromEntry(c.Providers[0], false, true)
	if len(view.SupportedEfforts) != 0 {
		t.Fatal("fixture should inherit backend options without persisted levels")
	}
	for _, ref := range []string{"", c.DefaultModel} {
		c.Agent.SubagentModel = ref
		reasoning := view.ModelCapabilities[0].Reasoning
		if reasoning == nil || len(reasoning.Options) == 0 {
			t.Fatal("settings must expose inferred reasoning options")
		}
		for _, option := range reasoning.Options {
			if err := setSubagentEffortConfig(c, option.ID); err != nil {
				t.Fatalf("displayed option %q rejected for %q: %v", option.ID, ref, err)
			}
		}
	}
	c.Agent.SubagentModel = "gateway/deepseek-v4-pro"
	reasoning := view.ModelCapabilities[1].Reasoning
	if reasoning == nil || len(reasoning.Options) != 0 {
		t.Fatal("model-level reasoning=none must expose no selectable effort")
	}
	if err := setSubagentEffortConfig(c, "high"); err == nil {
		t.Fatal("disabled model accepted high")
	}
	if err := setSubagentEffortConfig(c, ""); err != nil || c.Agent.SubagentEffort != "" {
		t.Fatalf("cannot clear stale effort: %v", err)
	}
}

func TestProviderRenamePreservesDefaultModelAndInferredEffort(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("REASONIX_HOME", t.TempDir())
	app := NewApp()
	view := ProviderView{
		Name: "gateway", Kind: "openai", BaseURL: "https://api.deepseek.com",
		Models:  []string{"deepseek-v4-flash", "deepseek-v4-pro"},
		Default: "deepseek-v4-pro", DefaultEffort: "low",
	}
	if err := app.SaveProvider(view); err != nil {
		t.Fatal(err)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	entry, ok := cfg.Provider("gateway")
	if !ok {
		t.Fatal("saved provider missing")
	}
	view = providerViewFromEntry(*entry, false, true)
	name := "Renamed connection"
	view.DisplayName = &name
	if err := app.SaveProvider(view); err != nil {
		t.Fatal(err)
	}
	cfg = config.LoadForEdit(config.UserConfigPath())
	entry, ok = cfg.Provider("gateway")
	if !ok || entry.DefaultModel() != "deepseek-v4-pro" || entry.DefaultEffort != "low" {
		t.Fatalf("rename lost persisted defaults: %+v", entry)
	}
	if len(entry.SupportedEfforts) != 0 {
		t.Fatal("rename froze automatically inferred options")
	}
	resolved, ok := cfg.ResolveModel("gateway")
	if !ok || resolved.Model != "deepseek-v4-pro" {
		t.Fatal("provider-only reference changed its selected model")
	}
	if got := config.ResolveReasoningView(resolved); got.Default != "low" || got.Error != "" {
		t.Fatalf("saved default does not match runtime reasoning: %+v", got)
	}
}
