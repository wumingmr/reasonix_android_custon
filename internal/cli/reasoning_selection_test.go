package cli

import (
	"reasonix/internal/boot"
	"reasonix/internal/config"
	"testing"
)

func TestCLISwitchAndReloadKeepEffortBoundToCurrentModel(t *testing.T) {
	cfg := &config.Config{Providers: []config.ProviderEntry{{Name: "gateway", Kind: "openai", BaseURL: "https://unknown.invalid/v1", Models: []string{"a", "b"}}}}
	high := "high"
	overrides := cliBuildOverrides{Effort: &high, EffortModel: "gateway/a"}
	for _, model := range []string{"gateway/b", "gateway/a", "gateway/a"} {
		overrides = overrides.forSelection(cfg, controllerBuildSpec{ModelRef: model})
		if overrides.Effort != nil {
			t.Fatal("switch/reload revived another model's effort")
		}
		if err := boot.ValidateReasoningSnapshot(cfg, cliProfileBuildOptions(model, 0, false, nil, overrides)); err != nil {
			t.Fatal(err)
		}
	}
	overrides = overrides.forSelection(cfg, controllerBuildSpec{ModelRef: "gateway/a", EffortOverride: &high})
	if err := boot.ValidateReasoningSnapshot(cfg, cliProfileBuildOptions("gateway/a", 0, false, nil, overrides)); err == nil {
		t.Fatal("explicit invalid effort was hidden")
	}
}
