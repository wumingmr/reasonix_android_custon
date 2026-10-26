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
	fileencoding "reasonix/internal/fileutil/encoding"
)

func TestOpenCodeGoInferenceStartupPreservesConnectionAndDeletion(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.toml")
	raw := `config_version = 12 # keep comment
default_model = "go/deepseek-v4-flash"
future_root = { keep = true }
[[providers]]
name = "go"
kind = "openai"
base_url = "https://opencode.ai/zen/go/v1"
request_url = "https://opencode.ai/zen/go/v1/chat/completions"
models_url = "https://opencode.ai/zen/go/v1/models"
models = ["deepseek-v4-flash", "private-model"]
default = "deepseek-v4-flash"
api_key_env = "USER_GO_KEY"
headers = { X-User = "keep" }
future_provider = { choice = "keep" }
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, err := ApplyUserConfigUpgradesOnStartup(path); err != nil || !changed {
		t.Fatalf("upgrade=%v err=%v", changed, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		t.Fatal(err)
	}
	p := &cfg.Providers[0]
	if p.BaseURL != "https://opencode.ai/inference/go/openai/v1" || p.RequestURL != p.BaseURL+"/chat/completions" || p.ModelsURL != "https://opencode.ai/inference/go/v1/models" {
		t.Fatalf("stale endpoints: %+v", p)
	}
	if !reflect.DeepEqual(p.Models, []string{"deepseek-v4-flash", "private-model", "deepseek-v4.1-flash"}) || p.Default != "deepseek-v4-flash" || cfg.DefaultModel != "go/deepseek-v4-flash" || p.APIKeyEnv != "USER_GO_KEY" || p.Headers["X-User"] != "keep" {
		t.Fatalf("lost connection settings: %+v", cfg)
	}
	for _, keep := range []string{"# keep comment", "future_root = { keep = true }", "future_provider = { choice = \"keep\" }"} {
		if !strings.Contains(string(data), keep) {
			t.Fatalf("lost %s", keep)
		}
	}
	// User deletions and custom endpoints after the one-time upgrade survive restarts.
	p.Models = p.Models[:2]
	p.RequestURL = "https://user.example/chat/completions"
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if changed, err := ApplyUserConfigUpgradesOnStartup(path); err != nil || changed {
		t.Fatalf("reapplied upgrade=%v err=%v", changed, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("post-upgrade user edits were changed")
	}
}

func TestOpenCodeGoInferencePresetsAndModelDiscovery(t *testing.T) {
	for _, id := range []string{"opencode-go-recommended", "opencode-go", "opencode-go-anthropic", "opencode-go-responses"} {
		p, _ := CuratedProviderPreset(id)
		for _, e := range p.Entries {
			want := "https://opencode.ai/inference/go/openai/v1"
			if e.Kind == "anthropic" {
				want = "https://opencode.ai/inference/go/anthropic"
			}
			if e.BaseURL != want {
				t.Fatalf("%s: base=%s want=%s", id, e.BaseURL, want)
			}
			if e.Kind == "openai" && !e.HasModel("deepseek-v4.1-flash") {
				t.Fatalf("%s missing V4.1 Flash", id)
			}
			urls, err := BuildModelFetchURLs(e.BaseURL, "")
			if err != nil || !reflect.DeepEqual(urls, []string{"https://opencode.ai/inference/go/v1/models"}) {
				t.Fatalf("%s: models=%v err=%v", id, urls, err)
			}
		}
	}
}

func TestOpenCodeGoInferenceUpgradeScope(t *testing.T) {
	for _, tc := range []struct {
		name, kind, base, extra, model, want string
		add                                  bool
	}{
		{"anthropic", "anthropic", "https://opencode.ai/zen/go", `request_url = "https://opencode.ai/zen/go/v1/messages"`, "qwen3.7-plus", "https://opencode.ai/inference/go/anthropic", false},
		{"responses", "responses", "https://opencode.ai/zen/go/v1", `request_url = "https://opencode.ai/zen/go/v1/responses"`, "grok-4.5", "https://opencode.ai/inference/go/openai/v1", false},
		{"legacy search", "anthropic", "https://opencode.ai/zen/go", "web_search = true", "deepseek-v4-flash", "https://opencode.ai/inference/go/anthropic", false},
		{"chat alias", "openai", "https://opencode.ai/zen/go/v1", `chat_url = "https://opencode.ai/zen/go/v1/chat/completions"`, "deepseek-v4-pro", "https://opencode.ai/inference/go/openai/v1", true},
		{"already new", "openai", "https://opencode.ai/inference/go/openai/v1", "", "deepseek-v4.1-flash", "https://opencode.ai/inference/go/openai/v1", true},
		{"custom base", "openai", "https://relay.example/v1", "", "deepseek-v4-flash", "https://relay.example/v1", false},
		{"custom request", "openai", "https://opencode.ai/zen/go/v1", `request_url = "https://opencode.ai/custom/chat/completions"`, "deepseek-v4-flash", "https://opencode.ai/zen/go/v1", false},
		{"custom query", "openai", "https://opencode.ai/zen/go/v1", `request_url = "https://opencode.ai/zen/go/v1/chat/completions?route=user"`, "deepseek-v4-flash", "https://opencode.ai/zen/go/v1", false},
		{"custom body", "openai", "https://opencode.ai/zen/go/v1", `extra_body = { thinking = false }`, "deepseek-v4-flash", "https://opencode.ai/zen/go/v1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "config_version = 12\n[[providers]]\nname = 'go'\nkind = '" + tc.kind + "'\nbase_url = '" + tc.base + "'\nmodel = '" + tc.model + "'\n" + tc.extra + "\n"
			next, changed, err := rewriteOpenCodeGoInferenceUpgrade(raw)
			if err != nil || !changed {
				t.Fatalf("upgrade=%v err=%v", changed, err)
			}
			var cfg Config
			if _, err := toml.Decode(next, &cfg); err != nil {
				t.Fatal(err)
			}
			p := cfg.Providers[0]
			if p.BaseURL != tc.want || p.HasModel("deepseek-v4.1-flash") != tc.add || p.DefaultModel() != tc.model || p.Model != tc.model {
				t.Fatalf("lost selection or upgraded wrong endpoint: %+v", p)
			}
			if tc.name == "legacy search" && !EffectiveWebSearch(&p) {
				t.Fatal("lost search capability")
			}
			if tc.base == tc.want && tc.name != "already new" {
				if strings.Replace(next, "config_version = 13", "config_version = 12", 1) != raw {
					t.Fatal("custom connection was rewritten")
				}
			}
		})
	}
}

func TestOpenCodeGoInferenceUpgradeInlinePricesAndAtomicRetry(t *testing.T) {
	raw := `config_version = 12
future_root = { keep = true }
providers = [{name = "go", kind = "openai", base_url = "https://opencode.ai/zen/go/v1", model = "deepseek-v4-pro", price = { input = 9.0, output = 27.0, cache_hit = 0.3, currency = "¥" }, future = { keep = "yes" }}]
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	failed := func(string, []byte, os.FileMode) error { return errors.New("interrupted write") }
	if changed, err := upgradeOpenCodeGoInferenceFileLocked(path, failed); err == nil || changed {
		t.Fatalf("upgrade=%v err=%v", changed, err)
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != raw {
		t.Fatal("failed write damaged original config")
	}
	if changed, err := upgradeOpenCodeGoInferenceFileLocked(path, fileutil.AtomicWriteFile); err != nil || !changed {
		t.Fatalf("retry=%v err=%v", changed, err)
	}
	var cfg Config
	if _, err := decodeTOMLFile(path, &cfg); err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers[0]
	price := p.PriceForModel("deepseek-v4.1-flash")
	if p.DefaultModel() != "deepseek-v4-pro" || p.Price.Input != 9 || price == nil || price.Input != 0.3 || price.Currency != "$" {
		t.Fatalf("lost price provenance: %+v new=%+v", p, price)
	}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTOMLFile(path, &Config{}); err != nil {
		t.Fatalf("current writer corrupted migration: %v", err)
	}
}

func TestOpenCodeGoInferenceUpgradePreservesWindowsEncoding(t *testing.T) {
	for _, encoding := range []fileencoding.Kind{fileencoding.UTF8BOM, fileencoding.UTF16LE, fileencoding.UTF16BE} {
		raw := "config_version = 12\r\n[[providers]]\r\nname = 'go'\r\nkind = 'openai'\r\nbase_url = 'https://opencode.ai/zen/go/v1'\r\nmodel = 'deepseek-v4.1-flash'\r\n"
		encoded, err := fileencoding.Encode(raw, encoding)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		if changed, err := upgradeOpenCodeGoInferenceFileLocked(path, fileutil.AtomicWriteFile); err != nil || !changed {
			t.Fatalf("upgrade=%v err=%v", changed, err)
		}
		result, _ := os.ReadFile(path)
		kind, data := fileencoding.Detect(result)
		decoded := string(fileencoding.Decode(data, kind))
		if kind != encoding || strings.Contains(strings.ReplaceAll(decoded, "\r\n", ""), "\n") || !strings.Contains(decoded, "/inference/go/openai/v1") {
			t.Fatalf("encoding=%v, text=%q", kind, decoded)
		}
	}
}
