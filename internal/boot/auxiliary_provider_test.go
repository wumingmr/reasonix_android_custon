package boot

import (
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/provider"
)

func TestAuxiliaryProviderPluginName(t *testing.T) {
	for _, test := range []struct {
		ref, want string
	}{
		{"plugin/providerdemo/fake/x", "providerdemo"},
		{"plugin/providerdemo/fake", ""},
		{"openai/gpt-5", ""},
		{"", ""},
	} {
		if got := auxiliaryProviderPluginName(test.ref); got != test.want {
			t.Fatalf("auxiliaryProviderPluginName(%q) = %q, want %q", test.ref, got, test.want)
		}
	}
}

func TestAcquireAuxiliaryProviderConfigModelNeedsNoExtensionRuntime(t *testing.T) {
	cfg := &config.Config{
		DefaultModel: "test/title-model",
		Providers: []config.ProviderEntry{{
			Name: "test", Kind: "openai", Model: "title-model",
			BaseURL: "https://example.invalid",
		}},
	}
	handle, err := AcquireAuxiliaryProvider(t.Context(), AuxiliaryProviderRequest{
		Config: cfg, SessionID: "cold-session", WorkspaceRoot: t.TempDir(), ModelRef: "test/title-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if handle.manager != nil {
		t.Fatal("config-backed auxiliary provider started extension sidecars")
	}
	if _, err := handle.Resolver.Resolve(provider.Selection{Ref: "test/title-model"}); err != nil {
		t.Fatalf("resolve config-backed model: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("second Close must be idempotent: %v", err)
	}
}

func TestAcquireAuxiliaryProviderRejectsUnavailableRecordedModel(t *testing.T) {
	cfg := &config.Config{
		DefaultModel: "test/title-model",
		Providers: []config.ProviderEntry{{
			Name: "test", Kind: "openai", Model: "title-model",
			BaseURL: "https://example.invalid",
		}},
	}
	handle, err := AcquireAuxiliaryProvider(t.Context(), AuxiliaryProviderRequest{
		Config: cfg, SessionID: "cold-session", WorkspaceRoot: t.TempDir(), ModelRef: "missing/model",
	})
	if handle != nil {
		_ = handle.Close()
		t.Fatal("unavailable recorded model returned a provider handle")
	}
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("AcquireAuxiliaryProvider unavailable model error = %v", err)
	}
}
