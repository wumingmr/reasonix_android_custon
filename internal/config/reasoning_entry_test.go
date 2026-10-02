package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"reasonix/internal/provider"
)

func TestLegacyReasoningDefaultsStaySeparateFromSavedOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	raw := `[[providers]]
name = "renamed-gateway"
kind = "openai"
base_url = "https://tokenrhythm.studio/v1"
models = ["deepseek-flash", "glm-5.1"]
future_field = "preserve-me"
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := LoadForEdit(path)
	baseline := cfg.ModelSettingsBaseline()
	for _, tc := range []struct{ model, protocol, effort string }{
		{"deepseek-flash", "deepseek", "high"}, {"glm-5.1", "glm", "enabled"},
	} {
		e, ok := cfg.ResolveModel("renamed-gateway/" + tc.model)
		if !ok {
			t.Fatal(tc.model)
		}
		if got := ReasoningProtocolForEntry(e); got != tc.protocol {
			t.Fatalf("protocol = %q", got)
		}
		if err := ReasoningCapabilityForEntry(e).Validate(e.Model, tc.effort); err != nil {
			t.Fatal(err)
		}
		// Catalog/settings callers that have not used ResolveModel must agree.
		unresolved := cfg.Providers[0]
		unresolved.Model = tc.model
		if !reflect.DeepEqual(ResolveReasoningView(&unresolved), ResolveReasoningView(e)) {
			t.Fatalf("UI/runtime disagree for %s", tc.model)
		}
	}
	cfg.Providers[0].DisplayName = "My gateway"
	if err := cfg.SaveModelSettingsTo(path, baseline); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "supported_efforts") || strings.Contains(string(saved), "reasoning_protocol") || !strings.Contains(string(saved), "preserve-me") {
		t.Fatalf("automatic capability leaked into saved user overrides: %s", saved)
	}
	// A subsequent catalog correction is visible to the same old connection.
	entry := cfg.Providers[0]
	entry.Model = "deepseek-flash"
	entry.ModelOverrides = map[string]ProviderModelOverride{"deepseek-flash": {SupportedEfforts: []string{"high"}, DefaultEffort: "high"}}
	cap := ReasoningCapabilityForEntry(&entry)
	if !reflect.DeepEqual(cap.IDs(), []string{"high"}) || cap.Validate(entry.Model, "max") == nil {
		t.Fatalf("explicit model vocabulary lost: %+v", cap)
	}
	entry.ModelOverrides = nil
	entry.SupportedEfforts = []string{"custom"}
	entry.DefaultEffort = "custom"
	if cap := ReasoningCapabilityForEntry(&entry); !reflect.DeepEqual(cap.IDs(), []string{"custom"}) {
		t.Fatal(cap)
	}
}

func TestReasoningDefaultsRespectEndpointProtocolAndExactModel(t *testing.T) {
	base := ProviderEntry{Name: "token-rhythm", Kind: "openai", BaseURL: "https://tokenrhythm.studio/v1", Model: "deepseek-flash"}
	for _, tc := range []struct {
		name  string
		edit  func(*ProviderEntry)
		state string
	}{
		{"renamed", func(e *ProviderEntry) { e.Name = "my-account" }, "supported"},
		{"unknown model", func(e *ProviderEntry) { e.Model = "DeepSeek-Flash" }, "unknown"},
		{"custom endpoint", func(e *ProviderEntry) { e.BaseURL = "https://relay.invalid/v1" }, "unknown"},
		{"request override", func(e *ProviderEntry) { e.RequestURL = "https://relay.invalid/chat/completions" }, "unknown"},
		{"disabled", func(e *ProviderEntry) { e.ReasoningProtocol = "none" }, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := base
			tc.edit(&e)
			cap := ReasoningCapabilityForEntry(&e)
			if cap.State() != tc.state {
				t.Fatalf("cap = %+v", cap)
			}
			if err := cap.Validate(e.Model, ""); err != nil {
				t.Fatal(err)
			}
			if tc.state == "unknown" {
				var diagnostic *provider.UnsupportedReasoningEffort
				err := cap.Validate(e.Model, "high")
				if !errors.As(err, &diagnostic) || !diagnostic.Unknown || !strings.Contains(err.Error(), "Select auto") {
					t.Fatalf("diagnostic = %v", err)
				}
			}
		})
	}
	base.ReasoningProtocol = "glm"
	if cap := ReasoningCapabilityForEntry(&base); !reflect.DeepEqual(cap.IDs(), []string{"enabled", "disabled"}) {
		t.Fatal(cap)
	}
}

func TestPresetReasoningIsInheritedAndExplicitDeclarationsRoundTrip(t *testing.T) {
	preset, _ := CuratedProviderPreset("token-rhythm")
	e := preset.Entries[0]
	if len(e.ModelOverrides["deepseek-flash"].SupportedEfforts) != 0 {
		t.Fatal("preset froze generated levels")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := &Config{}
	if err := cfg.UpsertProvider(e); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	loaded := LoadForEdit(path)
	resolved, _ := loaded.ResolveModel("token-rhythm/deepseek-flash")
	if err := ReasoningCapabilityForEntry(resolved).Validate("deepseek-flash", "high"); err != nil {
		t.Fatal(err)
	}
	e.ModelOverrides["deepseek-flash"] = ProviderModelOverride{ReasoningProtocol: "none"}
	if err := cfg.UpsertProvider(e); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	resolved, _ = LoadForEdit(path).ResolveModel("token-rhythm/deepseek-flash")
	if cap := ReasoningCapabilityForEntry(resolved); cap.State() != "unsupported" {
		t.Fatal(cap)
	}
}

func TestSessionEffortIsBoundToExactModelIdentity(t *testing.T) {
	cfg := &Config{Providers: []ProviderEntry{{Name: "a", Models: []string{"one", "two"}, Default: "one"}, {Name: "b", Model: "one"}}}
	for _, tc := range []struct {
		from, to string
		keep     bool
	}{
		{"a/one", "a/one", true}, {"a", "a/one", true}, {"a/one", "a/two", false},
		{"a/one", "b/one", false}, {"plugin/foo/model", "plugin/bar/model", false},
	} {
		value := "invalid-explicit-level"
		got := RebindSessionEffort(cfg, tc.from, tc.to, &value)
		if (got != nil) != tc.keep {
			t.Fatalf("%s -> %s: %v", tc.from, tc.to, got)
		}
		if got != nil && (*got != value || got == &value) {
			t.Fatal("same-route explicit intent must be independently retained")
		}
	}
}

func TestReasoningSnapshotSupportsOlderReadersAndWriters(t *testing.T) {
	preset, _ := CuratedProviderPreset("token-rhythm")
	e := preset.Entries[0]
	e.ModelOverrides["deepseek-flash"] = ProviderModelOverride{DefaultEffort: "max", ContextWindow: 123456}
	cfg := &Config{Providers: []ProviderEntry{e}}
	body := RenderTOML(cfg)
	// An older reader knows the original fields and ignores the optional marker.
	type oldOverride struct {
		ReasoningProtocol string   `toml:"reasoning_protocol"`
		SupportedEfforts  []string `toml:"supported_efforts"`
		DefaultEffort     string   `toml:"default_effort"`
		ContextWindow     int      `toml:"context_window"`
	}
	var old struct {
		Providers []struct {
			Name           string                 `toml:"name"`
			Kind           string                 `toml:"kind"`
			BaseURL        string                 `toml:"base_url"`
			Models         []string               `toml:"models"`
			ModelOverrides map[string]oldOverride `toml:"model_overrides"`
		} `toml:"providers"`
	}
	if _, err := toml.Decode(body, &old); err != nil {
		t.Fatal(err)
	}
	ov := old.Providers[0].ModelOverrides["deepseek-flash"]
	if ov.ReasoningProtocol != "deepseek" || len(ov.SupportedEfforts) != 4 || ov.DefaultEffort != "max" {
		t.Fatal(ov)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	current := LoadForEdit(path)
	explicit := current.Providers[0].ModelOverrides["deepseek-flash"]
	if explicit.DefaultEffort != "max" || explicit.ContextWindow != 123456 || len(explicit.SupportedEfforts) != 0 || explicit.ReasoningProtocol != "" {
		t.Fatalf("generated fields froze or user fields lost: %+v", explicit)
	}
	// A legacy editor changes the vocabulary and drops the unknown marker.
	ov.SupportedEfforts, ov.DefaultEffort = []string{"high"}, "high"
	old.Providers[0].ModelOverrides["deepseek-flash"] = ov
	var edited strings.Builder
	if err := toml.NewEncoder(&edited).Encode(old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(edited.String()), 0600); err != nil {
		t.Fatal(err)
	}
	resolved, _ := LoadForEdit(path).ResolveModel("token-rhythm/deepseek-flash")
	if cap := ReasoningCapabilityForEntry(resolved); !reflect.DeepEqual(cap.IDs(), []string{"high"}) {
		t.Fatalf("legacy edit lost: %+v", cap)
	}
	// Editors retaining unknown fields also retain manual reasoning changes.
	snapshot := reasoningCompatibilitySnapshot(e).ModelOverrides["deepseek-flash"]
	snapshot.SupportedEfforts = []string{"custom"}
	if got := explicitModelReasoning(snapshot); !reflect.DeepEqual(got.SupportedEfforts, []string{"custom"}) || got.ReasoningProtocol != "deepseek" {
		t.Fatalf("changed snapshot erased: %+v", got)
	}
}

func TestReasoningSnapshotDoesNotShadowConnectionEditsInOlderReaders(t *testing.T) {
	e := ProviderEntry{Name: "gateway", Kind: "openai", BaseURL: "https://tokenrhythm.studio/v1", Models: []string{"deepseek-flash"}, ReasoningProtocol: "deepseek", SupportedEfforts: []string{"high"}, DefaultEffort: "high"}
	var previousReader Config
	if _, err := toml.Decode(RenderTOML(&Config{Providers: []ProviderEntry{e}}), &previousReader); err != nil {
		t.Fatal(err)
	}
	ov := previousReader.Providers[0].ModelOverrides["deepseek-flash"]
	if ov.ReasoningProtocol != "" || len(ov.SupportedEfforts) != 0 || ov.DefaultEffort != "" {
		t.Fatalf("snapshot would shadow a legacy editor's provider-level changes: %+v", ov)
	}
}

func TestSavingResolvedRuntimePreservesReasoningInheritance(t *testing.T) {
	raw := ProviderEntry{Name: "gateway", Kind: "openai", BaseURL: "https://tokenrhythm.studio/v1", Model: "deepseek-flash", DefaultEffort: "max"}
	runtime := ResolveReasoningEntry(&raw)
	runtime.Effort = "low"
	cfg := &Config{}
	if err := cfg.UpsertProvider(*runtime); err != nil {
		t.Fatal(err)
	}
	saved := cfg.Providers[0]
	if saved.ReasoningProtocol != "" || len(saved.SupportedEfforts) != 0 || saved.DefaultEffort != "max" || saved.Effort != "low" {
		t.Fatalf("runtime defaults froze or explicit values lost: %+v", saved)
	}
	runtime.SupportedEfforts = []string{"low"}
	if err := cfg.UpsertProvider(*runtime); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Providers[0].SupportedEfforts, []string{"low"}) {
		t.Fatal("post-resolution edit lost")
	}
}
