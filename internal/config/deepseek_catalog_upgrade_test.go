package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"reasonix/internal/fileutil"
)

func TestDeepSeekCatalogUpgradePreservesSelectionsAndDeletion(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic", "responses"} {
		for _, singular := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/singular=%v", kind, singular), func(t *testing.T) {
				base := "https://api.deepseek.com"
				if kind == "anthropic" {
					base += "/anthropic"
				}
				models := `models = ["custom-ID", "deepseek-v4-pro", "deepseek-v4-flash"]`
				want := []string{"custom-ID", "deepseek-v4-pro", "deepseek-v4-flash", "deepseek-flash"}
				if singular {
					models = `model = "deepseek-v4-pro"`
					want = []string{"deepseek-v4-pro", "deepseek-flash"}
				}
				raw := fmt.Sprintf(`config_version = 10 # schema
default_model = "renamed/deepseek-v4-pro"
future_root = { choice = "keep" }
[[providers]]
name = "renamed"
kind = %q
base_url = %q
%s
default = "deepseek-v4-pro"
api_key_env = "USER_DEEPSEEK_KEY"
vision_models = []
web_search = false
future_provider = { value = "keep" }
[providers.model_overrides.deepseek-v4-pro]
default_effort = "max"
future_override = 42
`, kind, base, models)
				path := filepath.Join(t.TempDir(), "config.toml")
				if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
				if changed, err := ApplyUserConfigUpgradesOnStartup(path); err != nil || !changed {
					t.Fatalf("upgrade=%v err=%v", changed, err)
				}
				var cfg Config
				if _, err := decodeTOMLFile(path, &cfg); err != nil {
					t.Fatal(err)
				}
				p := cfg.Providers[0]
				if cfg.ConfigVersion != Default().ConfigVersion || !reflect.DeepEqual(p.Models, want) ||
					p.DefaultModel() != "deepseek-v4-pro" || cfg.DefaultModel != "renamed/deepseek-v4-pro" ||
					p.Kind != kind || p.APIKeyEnv != "USER_DEEPSEEK_KEY" || len(p.VisionModels) != 0 || *p.WebSearch {
					t.Fatalf("lost settings: version=%d default=%s provider=%+v", cfg.ConfigVersion, cfg.DefaultModel, p)
				}
				loaded := LoadForEdit(path)
				entry, ok := loaded.ResolveModel("renamed/deepseek-v4-pro")
				if !ok || entry.Model != "deepseek-v4-pro" || entry.DefaultEffort != "max" {
					t.Fatalf("old selection changed: %+v %v", entry, ok)
				}
				before, _ := os.ReadFile(path)
				for _, kept := range []string{`# schema`, `future_root = { choice = "keep" }`, `future_provider = { value = "keep" }`, `future_override = 42`} {
					if !strings.Contains(string(before), kept) {
						t.Fatalf("lost %s", kept)
					}
				}
				// Save through the ordinary settings writer after removing the new
				// option. The version travels with the user's remaining choices.
				loaded.Providers[0].Models = want[:len(want)-1]
				if err := loaded.SaveTo(path); err != nil {
					t.Fatal(err)
				}
				before, _ = os.ReadFile(path)
				for range 2 {
					if changed, err := ApplyUserConfigUpgradesOnStartup(path); err != nil || changed {
						t.Fatalf("reapplied upgrade: %v %v", changed, err)
					}
				}
				after, _ := os.ReadFile(path)
				if string(after) != string(before) || LoadForEdit(path).Providers[0].HasModel("deepseek-flash") {
					t.Fatal("deleted option reappeared")
				}
			})
		}
	}
}

func TestDeepSeekCatalogUpgradeScopeAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, base, extra string
		add               bool
	}{
		{"implicit default", "https://api.deepseek.com", `models = ["deepseek-v4-pro"]`, true},
		{"already current", "https://api.deepseek.com", `models = ["deepseek-v4-pro", "deepseek-flash"]`, false},
		{"empty list", "https://api.deepseek.com", `models = []`, false},
		{"custom endpoint", "https://api.deepseek.com", "models = [\"deepseek-v4-pro\"]\nrequest_url = \"https://api.deepseek.com/custom/chat/completions\"", false},
		{"third party", "https://relay.example/v1", `models = ["deepseek-v4-pro"]`, false},
		{"lookalike host", "https://api.deepseek.com.evil.test", `models = ["deepseek-v4-pro"]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf("config_version = 10\n[[providers]]\nname = 'deepseek'\nkind = 'openai'\nbase_url = %q\n%s\n", tc.base, tc.extra)
			next, changed, err := rewriteDeepSeekCatalogUpgrade(raw)
			if err != nil || !changed {
				t.Fatalf("%v %v", changed, err)
			}
			var before, after Config
			_, _ = toml.Decode(raw, &before)
			_, _ = toml.Decode(next, &after)
			want := before.Providers[0].ModelList()
			if tc.add {
				want = append(want, "deepseek-flash")
			}
			if !reflect.DeepEqual(want, after.Providers[0].ModelList()) || before.Providers[0].DefaultModel() != after.Providers[0].DefaultModel() {
				t.Fatalf("unexpected models/default: %s", next)
			}
		})
	}
	for _, raw := range []string{"config_version = 10\n", "config_version = 11\n", "config_version = 999\n"} {
		next, _, err := rewriteDeepSeekCatalogUpgrade(raw)
		if err != nil || (raw != "config_version = 10\n" && next != raw) {
			t.Fatalf("version-only %q: %q %v", raw, next, err)
		}
	}
}

func TestDeepSeekCatalogUpgradeKeepsLegacySingularDefault(t *testing.T) {
	// Older pricing/layout upgrades load and save before the catalog migration.
	// Normalization there must not silently switch a provider-only selection.
	for _, version := range []int{5, 10} {
		path := filepath.Join(t.TempDir(), "config.toml")
		raw := fmt.Sprintf(`config_version = %d
default_model = "deepseek-flash"
[[providers]]
name = "deepseek-flash"
kind = "openai"
base_url = "https://api.deepseek.com"
model = "deepseek-v4-flash"
[desktop]
layout_style = "classic"
`, version)
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ApplyUserConfigUpgradesOnStartup(path); err != nil {
			t.Fatal(err)
		}
		c := LoadForEdit(path)
		p, ok := c.ResolveModel(c.DefaultModel)
		if !ok || p.Model != "deepseek-v4-flash" || !p.HasModel("deepseek-flash") {
			t.Fatalf("v%d switched the existing selection: %+v", version, p)
		}
	}
}

func TestDeepSeekCatalogUpgradeAtomicRetryAndUnknownInlineFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	raw := "\ufeff" + strings.ReplaceAll(`config_version = 10 # version
providers = [{name='deepseek-pro',kind='openai',base_url='https://api.deepseek.com',model='deepseek-v4-pro',price={cache_hit=0.3,input=9,output=27,currency='¥'},future={date=2026-09-21,array=[{a='b'}]}}] # keep inline
[desktop]
future = "keep"
`, "\n", "\r\n")
	if err := os.WriteFile(path, []byte(raw), 0o640); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("interrupted before rename")
	if changed, err := upgradeDeepSeekCatalogFileLocked(path, func(_ string, proposed []byte, _ os.FileMode) error {
		if !strings.Contains(string(proposed), "deepseek-flash") || !strings.Contains(string(proposed), "config_version = 11") {
			t.Fatal("models and marker were not in the same commit")
		}
		return failure
	}); changed || !errors.Is(err, failure) {
		t.Fatalf("failed commit: %v %v", changed, err)
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != raw {
		t.Fatal("interruption partially modified config")
	}
	if changed, err := upgradeDeepSeekCatalogFileLocked(path, fileutil.AtomicWriteFile); err != nil || !changed {
		t.Fatalf("retry: %v %v", changed, err)
	}
	got, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(got), "\ufeff") || strings.Contains(strings.ReplaceAll(string(got), "\r\n", ""), "\n") || !strings.Contains(string(got), "# keep inline") {
		t.Fatalf("lost encoding or comments: %q", got)
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("changed permissions: %o", info.Mode().Perm())
	}
	var c Config
	if _, err := decodeTOMLFile(path, &c); err != nil {
		t.Fatal(err)
	}
	p := c.Providers[0]
	if p.Model != "deepseek-v4-pro" || p.DefaultModel() != "deepseek-v4-pro" || p.Price.Input != 9 || !reflect.DeepEqual(p.Prices["deepseek-flash"], deepSeekV4FlashPriceCNY()) {
		t.Fatalf("price/default provenance lost: %+v", p)
	}
}

func TestDeepSeekCatalogUpgradePreservesCustomFlashPriceAndNestedTables(t *testing.T) {
	raw := `config_version = 10
[[providers]]
name = 'pro'
kind = 'openai'
base_url = 'https://api.deepseek.com'
models = [
  'deepseek-v4-pro',
] # keep selection comment
price = {input=9, output=27, currency='¥'}
[providers.prices."deepseek-flash"]
input = 123.4 # custom new-model rate
future_price = true
[providers.model_overrides."deepseek-v4-pro"]
context_window = 12345
[[providers]]
name = 'second'
kind = 'responses'
base_url = 'https://api.deepseek.com'
models = ['deepseek-v4-flash']
price = {input=9, output=27, currency='¥'}
prices = { 'deepseek-v4-flash' = {input=123.0, currency='¥'} }
[desktop]
future = '''[[providers]]
fake header'''
`
	next, _, err := rewriteDeepSeekCatalogUpgrade(raw)
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if _, err := toml.Decode(next, &c); err != nil {
		t.Fatal(err)
	}
	if c.Providers[0].Prices["deepseek-flash"].Input != 123.4 || !c.Providers[1].HasModel("deepseek-flash") || !strings.Contains(next, "# custom new-model rate") || !strings.Contains(next, "# keep selection comment") {
		t.Fatalf("lost custom fields: %s", next)
	}
}
