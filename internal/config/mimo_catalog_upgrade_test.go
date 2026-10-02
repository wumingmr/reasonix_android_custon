package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"reasonix/internal/fileutil"
)

func TestMimoCatalogUpgradePreservesSelectionsAndDeletion(t *testing.T) {
	raw := `config_version = 11 # schema
default_model = "mimo-api/mimo-v2.5-pro"
future_root = { choice = "keep" }
[[providers]]
name = "mimo-api"
kind = "openai"
base_url = "https://api.xiaomimimo.com/v1"
models = ["custom-ID", "mimo-v2.5-pro", "mimo-v2.5"]
default = "mimo-v2.5-pro"
api_key_env = "USER_MIMO_KEY"
vision_models = ["mimo-v2.5"]
future_provider = { value = "keep" }
[providers.prices.mimo-v2.5-pro]
cache_hit = 0.025
input = 3.0
output = 6.0
currency = "¥"
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := ApplyUserConfigUpgradesOnStartup(path); err != nil || !changed {
		t.Fatalf("upgrade=%v err=%v", changed, err)
	}
	cfg := LoadForEdit(path)
	p, ok := cfg.Provider("mimo-api")
	if !ok {
		t.Fatal("mimo-api missing after upgrade")
	}
	wantModels := []string{"custom-ID", "mimo-v2.5-pro", "mimo-v2.5", "mimo-v2.6-pro", "mimo-v2.6-flash"}
	wantVision := []string{"mimo-v2.5", "mimo-v2.6-pro", "mimo-v2.6-flash"}
	if cfg.ConfigVersion != Default().ConfigVersion || !reflect.DeepEqual(p.Models, wantModels) ||
		!reflect.DeepEqual(p.VisionModels, wantVision) || p.DefaultModel() != "mimo-v2.5-pro" ||
		cfg.DefaultModel != "mimo-api/mimo-v2.5-pro" || p.APIKeyEnv != "USER_MIMO_KEY" {
		t.Fatalf("lost settings: version=%d default=%s provider=%+v", cfg.ConfigVersion, cfg.DefaultModel, p)
	}
	if price := p.PriceForModel("mimo-v2.6-flash"); price == nil || price.Input != 1 || price.Output != 2 || price.CacheHit != 0.02 {
		t.Fatalf("mimo-v2.6-flash price = %+v", price)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{`# schema`, `future_root = { choice = "keep" }`, `future_provider = { value = "keep" }`} {
		if !strings.Contains(string(before), kept) {
			t.Fatalf("lost %s", kept)
		}
	}
	p.Models = []string{"custom-ID", "mimo-v2.5-pro", "mimo-v2.5", "mimo-v2.6-pro"}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	before, _ = os.ReadFile(path)
	for range 2 {
		if changed, err := ApplyUserConfigUpgradesOnStartup(path); err != nil || changed {
			t.Fatalf("reapplied upgrade: %v %v", changed, err)
		}
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) || LoadForEdit(path).Providers[0].HasModel("mimo-v2.6-flash") {
		t.Fatal("deleted MiMo model reappeared")
	}
}

func TestMimoCatalogUpgradeScopeAndCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name, kind, base, extra string
		add, addVision          bool
	}{
		{"payg openai", "openai", "https://api.xiaomimimo.com/v1", `models = ["mimo-v2.5-pro", "mimo-v2.5"]
vision_models = ["mimo-v2.5"]`, true, true},
		{"payg anthropic", "anthropic", "https://api.xiaomimimo.com/anthropic", `models = ["mimo-v2.5-pro"]`, true, false},
		{"token Singapore", "openai", "https://token-plan-sgp.xiaomimimo.com/v1", `models = ["mimo-v2.5"]`, true, false},
		{"token Europe", "anthropic", "https://token-plan-ams.xiaomimimo.com/anthropic", `models = ["mimo-v2.5-pro"]`, true, false},
		{"explicit vision off", "openai", "https://token-plan-cn.xiaomimimo.com/v1", `models = ["mimo-v2.5"]
vision_models = []`, true, false},
		{"custom endpoint", "openai", "https://relay.example/v1", `models = ["mimo-v2.5-pro"]`, false, false},
		{"custom request", "openai", "https://api.xiaomimimo.com/v1", `models = ["mimo-v2.5-pro"]
request_url = "https://api.xiaomimimo.com/custom"`, false, false},
		{"unrelated catalog", "openai", "https://api.xiaomimimo.com/v1", `models = ["custom-model"]`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "config_version = 11\n[[providers]]\nname = 'mimo'\nkind = '" + tc.kind + "'\nbase_url = '" + tc.base + "'\n" + tc.extra + "\n"
			next, changed, err := rewriteMimoCatalogUpgrade(raw)
			if err != nil || !changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			var cfg Config
			if _, err := toml.Decode(next, &cfg); err != nil {
				t.Fatal(err)
			}
			p := cfg.Providers[0]
			if p.HasModel("mimo-v2.6-pro") != tc.add || p.HasModel("mimo-v2.6-flash") != tc.add {
				t.Fatalf("models = %v, add=%v", p.ModelList(), tc.add)
			}
			if p.HasVisionModel("mimo-v2.6-pro") != tc.addVision || p.HasVisionModel("mimo-v2.6-flash") != tc.addVision {
				t.Fatalf("vision models = %v, add=%v", p.VisionModels, tc.addVision)
			}
		})
	}
}

func TestMimoCatalogUpgradeSingularPriceAndAtomicRetry(t *testing.T) {
	raw := `config_version = 11
[[providers]]
name = "mimo-token-plan-cn"
kind = "openai"
base_url = "https://token-plan-cn.xiaomimimo.com/v1"
model = "mimo-v2.5-pro"
price = { cache_hit = 9.0, input = 9.0, output = 9.0, currency = "¥" }
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	failedWrite := func(string, []byte, os.FileMode) error { return errors.New("interrupted") }
	if changed, err := upgradeMimoCatalogFileLocked(path, failedWrite); err == nil || changed {
		t.Fatalf("interrupted upgrade = %v, %v", changed, err)
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != raw {
		t.Fatal("failed migration changed the original file")
	}
	if changed, err := upgradeMimoCatalogFileLocked(path, fileutil.AtomicWriteFile); err != nil || !changed {
		t.Fatalf("retry = %v, %v", changed, err)
	}
	cfg := LoadForEdit(path)
	p := &cfg.Providers[0]
	if p.DefaultModel() != "mimo-v2.5-pro" {
		t.Fatalf("singular default changed to %q", p.DefaultModel())
	}
	for model, wantInput := range map[string]float64{"mimo-v2.6-pro": 3, "mimo-v2.6-flash": 1} {
		if price := p.PriceForModel(model); price == nil || price.Input != wantInput {
			t.Fatalf("%s inherited singular price: %+v", model, price)
		}
	}
}

func TestMimoCatalogUpgradeTOMLRepresentations(t *testing.T) {
	for name, raw := range map[string]string{
		"inline providers and prices": `providers = [{name="mimo", kind="openai", base_url="https://api.xiaomimimo.com/v1", models=["mimo-v2.5"], prices={"mimo-v2.5"={input=9.0, future="keep"}}}]`,
		"inline prices": `[[providers]]
name="mimo"
kind="openai"
base_url="https://api.xiaomimimo.com/v1"
models=["mimo-v2.5"]
prices={"mimo-v2.5"={input=9.0, future="keep"}}
`,
		"nested prices and adjacent providers": `[[providers]]
name="mimo"
kind="openai"
base_url="https://api.xiaomimimo.com/v1"
models=["mimo-v2.5"]
[providers.prices."mimo-v2.5"]
input=9.0
future="keep"
[[providers]]
name="other"
kind="openai"
base_url="https://relay.example/v1"
models=["custom"]
[providers.prices.custom]
input=7.0
[future]
value="keep"
`,
	} {
		t.Run(name, func(t *testing.T) {
			next, changed, err := rewriteMimoCatalogUpgrade("config_version=11\n" + raw)
			if err != nil || !changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			var cfg Config
			if _, err := toml.Decode(next, &cfg); err != nil {
				t.Fatal(err)
			}
			p := cfg.Providers[0]
			if !p.HasModel("mimo-v2.6-pro") || !p.HasModel("mimo-v2.6-flash") || p.Prices["mimo-v2.5"].Input != 9 {
				t.Fatalf("unexpected migrated provider: %+v", p)
			}
			if _, changed, err := rewriteMimoCatalogUpgrade(next); err != nil || changed {
				t.Fatalf("second upgrade changed=%v err=%v", changed, err)
			}
		})
	}
}
