package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A per-provider effort on the canonical member must not make the DeepSeek
// family non-canonicalizable, or the stored level is unreachable (#8337).
func TestDeepSeekEffortDoesNotBreakFamilyCanonicalization(t *testing.T) {
	body := `config_version = 5
default_model = "deepseek/deepseek-v4-flash"

[[providers]]
name = "deepseek"
kind = "anthropic"
base_url = "https://api.deepseek.com/anthropic"
models = ["deepseek-v4-flash", "deepseek-v4-pro"]
default = "deepseek-v4-flash"
api_key_env = "DEEPSEEK_API_KEY"
effort = "max"

[[providers]]
name = "deepseek-flash"
kind = "anthropic"
base_url = "https://api.deepseek.com/anthropic"
api_key_env = "DEEPSEEK_API_KEY"
`
	path := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c := LoadForEdit(path)
	if !canCanonicalizeLegacyDeepSeekProviders(c) {
		t.Fatal("a per-provider effort selection on the canonical member must not make the DeepSeek family non-canonicalizable")
	}
	p, ok := c.Provider("deepseek")
	if !ok {
		t.Fatal("canonical deepseek provider missing")
	}
	if got := p.Effort; got != "max" {
		t.Fatalf("stored effort = %q, want max", got)
	}
}

func TestDeepSeekLegacyMembersWithDifferentEffortsStaySeparate(t *testing.T) {
	body := `config_version = 5
default_model = "deepseek-pro/deepseek-v4-pro"

[[providers]]
name = "deepseek-flash"
kind = "anthropic"
base_url = "https://api.deepseek.com/anthropic"
model = "deepseek-v4-flash"
api_key_env = "DEEPSEEK_API_KEY"

[[providers]]
name = "deepseek-pro"
kind = "anthropic"
base_url = "https://api.deepseek.com/anthropic"
model = "deepseek-v4-pro"
api_key_env = "DEEPSEEK_API_KEY"
effort = "max"
`
	path := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c := LoadForEdit(path)
	if canCanonicalizeLegacyDeepSeekProviders(c) {
		t.Fatal("legacy members with different efforts must not merge: the merge keeps only the first member's effort")
	}
	p, ok := c.Provider("deepseek-pro")
	if !ok {
		t.Fatal("legacy deepseek-pro provider missing")
	}
	if got := p.Effort; got != "max" {
		t.Fatalf("deepseek-pro effort = %q, want max", got)
	}
}
