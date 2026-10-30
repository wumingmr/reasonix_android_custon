package cli

import (
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/i18n"
)

// Blank keeps the model's current window; a non-positive number clears it so
// the model inherits again. That is the only way the wizard can undo a window,
// and typing nothing must not silently do it.
func TestParseContextWindowAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		had  int
		want int
	}{
		{"blank keeps", "", 500_000, 500_000},
		{"blank with nothing keeps nothing", "  ", 0, 0},
		{"number sets", "1000000", 0, 1_000_000},
		{"zero clears", "0", 500_000, 0},
		{"negative clears", "-5", 500_000, 0},
		{"garbage keeps", "many", 500_000, 500_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseContextWindowAnswer(tc.raw, tc.had); got != tc.want {
				t.Fatalf("parse(%q, %d) = %d, want %d", tc.raw, tc.had, got, tc.want)
			}
		})
	}
}

// The menu line states the current answer, so the manager reads as a summary
// and not only as a list of actions.
func TestModelWindowsSummaryDescribesTheCurrentShape(t *testing.T) {
	shared := config.ProviderEntry{Name: "relay", Kind: "openai", Models: []string{"a", "b"}, ContextWindow: 100_000}
	if got := modelWindowsSummary(shared); !strings.Contains(got, "100000") {
		t.Fatalf("shared summary = %q", got)
	}
	none := config.ProviderEntry{Name: "relay", Kind: "openai", Models: []string{"a"}}
	if got := modelWindowsSummary(none); got != i18n.M.SetupModelWindowNone {
		t.Fatalf("summary for a connection with no window = %q", got)
	}
	perModel := config.ProviderEntry{
		Name: "relay", Kind: "openai", Models: []string{"a", "b", "c"},
		ModelOverrides: map[string]config.ProviderModelOverride{"a": {ContextWindow: 1_000}, "b": {ContextWindow: 2_000}},
	}
	if got := modelWindowsSummary(perModel); !strings.Contains(got, "2") {
		t.Fatalf("per-model summary = %q", got)
	}
}

// A window set through the setup session reaches the saved configuration and
// leaves every other field of the connection alone.
func TestSetupSessionPreservesPerModelWindows(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = []config.ProviderEntry{{
		Name: "relay", Kind: "openai", BaseURL: "https://relay.example/v1",
		Models: []string{"big", "small"}, Default: "big",
		SupportedEfforts: []string{"low", "high"}, DefaultEffort: "high",
	}}
	session := newProviderSetupSession(cfg)

	edited := cfg.Providers[0]
	if err := edited.SetModelLimits("big", config.ModelLimits{ContextWindow: 1_000_000, MaxOutputTokens: 32_768}); err != nil {
		t.Fatal(err)
	}
	if err := session.upsert([]config.ProviderEntry{edited}); err != nil {
		t.Fatal(err)
	}

	saved := cfg.Providers[0]
	limits, ok := saved.ModelLimits("big")
	if !ok || limits.ContextWindow != 1_000_000 || limits.MaxOutputTokens != 32_768 {
		t.Fatalf("saved limits = %+v ok=%v", limits, ok)
	}
	if _, ok := saved.ModelOverrides["small"]; ok {
		t.Fatal("a model nobody set gained an override")
	}
	if saved.ContextWindow != 0 {
		t.Fatalf("connection-wide window was written as %d", saved.ContextWindow)
	}
	if len(saved.SupportedEfforts) != 2 || saved.DefaultEffort != "high" {
		t.Fatalf("the effort ladder was lost: %+v", saved)
	}
}

// Per-provider actions must offer the per-model window editor, because it is
// the only terminal surface that can set one; hand-editing TOML is not an
// option most users of this build have.
func TestProviderActionsOfferPerModelWindows(t *testing.T) {
	entry := config.ProviderEntry{
		Name: "relay", Kind: "openai", BaseURL: "https://relay.example/v1",
		Models: []string{"a", "b"}, Default: "a", ContextWindow: 100_000,
	}
	items := providerActionItems(entry)
	index := -1
	for i, item := range items {
		if item.name == i18n.M.SetupModelWindows {
			index = i
			if item.desc == "" {
				t.Fatal("the window editor offers no summary of the current shape")
			}
		}
	}
	if index < 0 {
		t.Fatalf("per-model window editor missing from %v", items)
	}
	// The dispatcher addresses this action by a named position, so the menu and
	// the switch cannot drift apart.
	if index != providerActionWindows {
		t.Fatalf("window editor sits at %d, dispatcher expects %d", index, providerActionWindows)
	}
	if items[providerActionBack].name != i18n.M.SetupBack {
		t.Fatalf("Back is not at the position the dispatcher treats as Back: %v", items)
	}
}
