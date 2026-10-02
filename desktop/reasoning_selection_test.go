package main

import (
	"testing"

	"reasonix/internal/config"
)

func TestDraftEffortFollowsItsModel(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	baseline := app.defaultDraftSettings("global", "")
	high := "high"
	tab := &WorkspaceTab{ID: "active", model: "another/model", effort: &high}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.activeTabID = tab.ID
	if got := app.defaultDraftSettings("global", ""); got.Model != baseline.Model || got.Effort != "" {
		t.Fatalf("cross-model inherited effort: %+v", got)
	}
	tab.model = baseline.Model
	if got := app.defaultDraftSettings("global", ""); got.Effort != high {
		t.Fatalf("same model lost effort: %+v", got)
	}
}

func TestSettingsReasoningMetadataDoesNotBecomeAnOverride(t *testing.T) {
	e := config.ProviderEntry{Name: "legacy", Kind: "openai", BaseURL: "https://tokenrhythm.studio/v1", Models: []string{"deepseek-flash", "unknown"}}
	views := providerModelCapabilitiesForView(e, e.Models)
	if len(views) != 2 || views[0].Reasoning.State != "supported" || views[0].Reasoning.Default != "high" || views[1].Reasoning.State != "unknown" {
		t.Fatalf("views = %+v", views)
	}
	if len(e.ModelOverrides) != 0 || len(e.SupportedEfforts) != 0 {
		t.Fatal("metadata mutated config")
	}
}
