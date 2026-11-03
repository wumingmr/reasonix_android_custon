package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
)

func TestSetupReasoningSavesDeclarationsAndImagesTogether(t *testing.T) {
	s, path := setupImageFixture(t)
	catalog := setupImageTestCatalog()
	catalog[0].ReasoningInfo = &provider.ModelReasoningInfo{Protocol: "openai", Efforts: []string{"low", "high"}, Default: "low"}
	s.stageImageCatalog(s.cfg.Providers[0], catalog, time.Now())
	if s.cfg.Providers[0].ModelOverrides["image"].DiscoveredReasoning != nil {
		t.Fatal("discovery written before Save")
	}
	written, err := commitProviderSetupSession(s, path)
	if err != nil || !written {
		t.Fatalf("commit=%v %v", written, err)
	}
	loaded, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := loaded.ResolveModel("relay/image")
	if config.EffectiveEffort(e) != "low" {
		t.Fatal("saved reasoning default lost")
	}
	if config.NewModelCapabilityResolver().Resolve(e).State != config.CapabilitySupported {
		t.Fatal("reasoning save broke image cache")
	}
	before, _ := os.ReadFile(path)
	refresh := newProviderSetupSession(loaded)
	refresh.stageImageCatalog(loaded.Providers[0], setupImageTestCatalog(), time.Now())
	if _, err := commitProviderSetupSession(refresh, path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) == string(after) || strings.Contains(string(after), "discovered_reasoning") {
		t.Fatal("refresh missing metadata did not remove stale declaration")
	}
}

func TestSetupReasoningBoolDoesNotInventLevels(t *testing.T) {
	s, path := setupImageFixture(t)
	catalog := setupImageTestCatalog()
	yes := true
	catalog[0].ReasoningInfo = &provider.ModelReasoningInfo{Supported: &yes, Parameter: true}
	s.stageImageCatalog(s.cfg.Providers[0], catalog, time.Now())
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := loaded.ResolveModel("relay/image")
	if !config.ReasoningCapabilityForEntry(e).Unknown {
		t.Fatal("reasoning flag invented usable levels")
	}
}

func TestSetupReasoningEditorUsesStagedDiscovery(t *testing.T) {
	s, path := setupImageFixture(t)
	catalog := setupImageTestCatalog()
	catalog[0].ReasoningInfo = &provider.ModelReasoningInfo{Protocol: "openai", Efforts: []string{"low", "high"}, Default: "low"}
	s.stageImageCatalog(s.cfg.Providers[0], catalog, time.Now())
	old := selectSetupEffort
	t.Cleanup(func() { selectSetupEffort = old })
	calls := 0
	selectSetupEffort = func(_ string, items []menuItem) (int, error) {
		calls++
		if calls == 1 {
			return 0, nil
		}
		for i, item := range items {
			if item.name == "high" {
				return i, nil
			}
		}
		t.Fatal("staged discovery levels missing from editor")
		return 0, nil
	}
	editProviderModelEfforts(s, s.cfg.Providers[0])
	if s.cfg.Providers[0].ModelOverrides["image"].DiscoveredReasoning != nil {
		t.Fatal("editor prematurely wrote discovery")
	}
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := loaded.ResolveModel("relay/image")
	if config.EffectiveEffort(e) != "high" || len(e.SupportedEfforts) != 2 {
		t.Fatal("manual effort edit discarded discovery")
	}
}

func TestEffortCommandPersistsOnlySelectedModel(t *testing.T) {
	source, err := os.ReadFile("effort.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "SetModelEffort(entry.Name, entry.Model, &effort)") || strings.Contains(string(source), "SetProviderEffort") {
		t.Fatal("/effort still changes connection-wide setting")
	}
	items := providerActionItems(config.ProviderEntry{})
	if items[providerActionEfforts].name != i18n.M.SetupModelEfforts {
		t.Fatal("setup lacks per-model effort editor")
	}
}
