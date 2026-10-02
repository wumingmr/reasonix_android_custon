package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Point this at an isolated pre-v11 source checkout for actual cross-version
// read/write acceptance. The overlay adds only a disposable test to that code.
func TestDeepSeekCatalogPreviousReaderRoundTrip(t *testing.T) {
	checkout := os.Getenv("REASONIX_DEEPSEEK_PREVIOUS_CHECKOUT")
	if checkout == "" {
		t.Skip("set REASONIX_DEEPSEEK_PREVIOUS_CHECKOUT for previous-reader acceptance")
	}
	var err error
	checkout, err = filepath.EvalSymlinks(checkout)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err = filepath.Abs(checkout)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("REASONIX_HOME", dir)
	path := filepath.Join(dir, "config.toml")
	const raw = `config_version = 10
default_model = "deepseek/deepseek-v4-flash"
[[providers]]
name = "deepseek"
kind = "openai"
base_url = "https://api.deepseek.com"
models = ["deepseek-v4-flash", "deepseek-v4-pro"]
default = "deepseek-v4-flash"
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyUserConfigUpgradesOnStartup(path); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(dir, "previous_reader_test.go")
	const source = `package config
import ("os"; "slices"; "testing")
func TestDeepSeekPreviousReaderProbe(t *testing.T) {
 if Default().ConfigVersion != 10 { t.Fatal("probe requires a schema-v10 reader") }
 path := os.Getenv("REASONIX_COMPAT_CONFIG")
 c := LoadForEdit(path)
 p,ok := c.Provider("deepseek")
 if !ok || p.DefaultModel() != "deepseek-v4-flash" || c.ConfigVersion != 11 { t.Fatal("old reader lost selection or marker") }
 if os.Getenv("REASONIX_COMPAT_DELETE_FLASH") == "1" {
  p.Models = slices.DeleteFunc(p.Models,func(m string) bool { return m == "deepseek-flash" })
 } else if !p.HasModel("deepseek-flash") { t.Fatal("old reader lost the new option") }
 p.DisplayName = "old-reader-edited"
 if err := c.SaveToScope(path,RenderScopeFull); err != nil { t.Fatal(err) }
}`
	if err := os.WriteFile(probe, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay.json")
	data, err := json.Marshal(map[string]any{"Replace": map[string]string{filepath.Join(checkout, "internal/config/deepseek_previous_probe_test.go"): probe}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, deleted := range []string{"0", "1"} {
		cmd := exec.Command("go", "test", "-overlay", overlay, "./internal/config", "-run", "^TestDeepSeekPreviousReaderProbe$", "-v", "-count=1")
		cmd.Dir = checkout
		cmd.Env = append(os.Environ(), "REASONIX_COMPAT_CONFIG="+path, "REASONIX_COMPAT_DELETE_FLASH="+deleted)
		output, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "--- PASS: TestDeepSeekPreviousReaderProbe") {
			t.Fatalf("old reader: %v\n%s", err, output)
		}
		if changed, err := ApplyUserConfigUpgradesOnStartup(path); err != nil || changed {
			t.Fatalf("old writer lost migration marker: %v %v", changed, err)
		}
		c := LoadForEdit(path)
		p, ok := c.Provider("deepseek")
		if !ok || c.ConfigVersion != Default().ConfigVersion || p.DisplayName != "old-reader-edited" ||
			p.DefaultModel() != "deepseek-v4-flash" || p.HasModel("deepseek-flash") != (deleted == "0") {
			t.Fatalf("old reader round-trip lost choices: %+v", p)
		}
		if ref, ok := c.ResolveModel(c.DefaultModel); !ok || ref.Model != "deepseek-v4-flash" {
			t.Fatalf("old reader round-trip lost current reference: %+v", ref)
		}
	}
}
