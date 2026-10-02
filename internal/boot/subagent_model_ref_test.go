package boot

import (
	"testing"

	"reasonix/internal/config"
)

func TestSubagentBareModelStaysOnParentProvider(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = []config.ProviderEntry{
		{Name: "first", Kind: "openai", Models: []string{"shared-flash", "first-only"}},
		{Name: "relay", Kind: "openai", Models: []string{"shared-flash"}},
	}
	base, ok := cfg.ResolveModel("relay/shared-flash")
	if !ok {
		t.Fatal("relay/shared-flash should resolve")
	}
	for ref, want := range map[string]string{
		"shared-flash":       "relay/shared-flash",
		"first-only":         "first/first-only",
		"first/shared-flash": "first/shared-flash",
	} {
		_, got, err := subagentModelSelection(cfg, nil, base, ref)
		if err != nil || got != want {
			t.Fatalf("selection for %q = %q (%v), want %q", ref, got, err, want)
		}
	}
	if model, _ := subagentEffectiveIdentity(cfg, nil, "relay/shared-flash", base, "shared-flash", ""); model != "relay/shared-flash" {
		t.Fatalf("identity = %q, want relay/shared-flash", model)
	}
}
