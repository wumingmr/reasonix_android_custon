package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeGoInferencePreviousReaderRoundTrip(t *testing.T) {
	checkout := os.Getenv("REASONIX_OPENCODE_PREVIOUS_CHECKOUT")
	if checkout == "" {
		t.Skip("set REASONIX_OPENCODE_PREVIOUS_CHECKOUT for schema-v12 reader acceptance")
	}
	checkout, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("REASONIX_HOME", dir)
	path := filepath.Join(dir, "config.toml")
	raw := `config_version = 12
default_model = "go/deepseek-v4-pro"
[[providers]]
name = "go"
kind = "openai"
base_url = "https://opencode.ai/zen/go/v1"
request_url = "https://opencode.ai/zen/go/v1/chat/completions"
models_url = "https://opencode.ai/zen/go/v1/models"
models = ["deepseek-v4-pro"]
api_key_env = "USER_GO_KEY"
price = { input = 9.0, output = 27.0, cache_hit = 0.3, currency = "¥" }
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyUserConfigUpgradesOnStartup(path); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(dir, "previous_reader_test.go")
	source := `package config
import ("os"; "slices"; "testing")
func TestOpenCodeInferencePreviousReaderProbe(t *testing.T) {
 if Default().ConfigVersion != 12 { t.Fatal("probe requires schema-v12 reader") }
 path := os.Getenv("REASONIX_COMPAT_CONFIG")
 c := LoadForEdit(path)
 p,ok := c.Provider("go")
 if !ok || c.ConfigVersion != 13 || p.BaseURL != "https://opencode.ai/inference/go/openai/v1" || p.DefaultModel() != "deepseek-v4-pro" { t.Fatal("old reader lost route, selection or marker") }
 if p.RequestURL != p.BaseURL+"/chat/completions" || p.ModelsURL != "https://opencode.ai/inference/go/v1/models" { t.Fatal("old reader lost request/discovery endpoint") }
 if os.Getenv("REASONIX_COMPAT_DELETE_FLASH") == "1" {
  p.Models = slices.DeleteFunc(p.Models,func(m string) bool { return m == "deepseek-v4.1-flash" })
 } else if !p.HasModel("deepseek-v4.1-flash") { t.Fatal("old reader lost new model") }
 p.DisplayName = "old-reader-edited"
 if err := c.SaveToScope(path,RenderScopeFull); err != nil { t.Fatal(err) }
}`
	if err := os.WriteFile(probe, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"Replace": map[string]string{filepath.Join(checkout, "internal/config/mimo_catalog_downgrade_test.go"): probe}})
	if err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlay, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, deleted := range []string{"0", "1"} {
		cmd := exec.Command("go", "test", "-overlay", overlay, "./internal/config", "-run", "^TestOpenCodeInferencePreviousReaderProbe$", "-v", "-count=1")
		cmd.Dir = checkout
		cmd.Env = append(os.Environ(), "REASONIX_COMPAT_CONFIG="+path, "REASONIX_COMPAT_DELETE_FLASH="+deleted)
		output, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "--- PASS: TestOpenCodeInferencePreviousReaderProbe") {
			t.Fatalf("old reader: %v\n%s", err, output)
		}
		if changed, err := ApplyUserConfigUpgradesOnStartup(path); err != nil || changed {
			t.Fatalf("old writer lost marker: %v %v", changed, err)
		}
		c := LoadForEdit(path)
		p, ok := c.Provider("go")
		if !ok || p.DisplayName != "old-reader-edited" || p.HasModel("deepseek-v4.1-flash") != (deleted == "0") || p.PriceForModel("deepseek-v4.1-flash").Input != 0.3 || p.APIKeyEnv != "USER_GO_KEY" || c.DefaultModel != "go/deepseek-v4-pro" || c.ConfigVersion != openCodeGoInferenceUpgradeVersion {
			t.Fatalf("old writer lost fields: %+v", p)
		}
		// All fields introduced by this migration already exist in schema 12.
		// Unknown-field preservation during migration is covered separately;
		// the previous full renderer never retained arbitrary unknown fields.
	}
}
