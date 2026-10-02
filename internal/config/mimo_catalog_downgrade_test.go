package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Use an isolated schema-v11 checkout to exercise the actual previous reader
// and writer, including deletion followed by a restart in the new version.
func TestMimoCatalogPreviousReaderRoundTrip(t *testing.T) {
	checkout := os.Getenv("REASONIX_MIMO_PREVIOUS_CHECKOUT")
	if checkout == "" {
		t.Skip("set REASONIX_MIMO_PREVIOUS_CHECKOUT for previous-reader acceptance")
	}
	checkout, err := filepath.Abs(checkout)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err = filepath.EvalSymlinks(checkout)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("REASONIX_HOME", dir)
	path := filepath.Join(dir, "config.toml")
	const raw = `config_version = 11
default_model = "mimo/mimo-v2.5-pro"
[[providers]]
name = "mimo"
kind = "openai"
base_url = "https://api.xiaomimimo.com/v1"
models = ["mimo-v2.5-pro", "mimo-v2.5"]
default = "mimo-v2.5-pro"
vision_models = ["mimo-v2.5"]
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
func TestMimoPreviousReaderProbe(t *testing.T) {
 if Default().ConfigVersion != 11 { t.Fatal("probe requires a schema-v11 reader") }
 path := os.Getenv("REASONIX_COMPAT_CONFIG")
 c := LoadForEdit(path)
 p,ok := c.Provider("mimo")
 if !ok || p.DefaultModel() != "mimo-v2.5-pro" || c.ConfigVersion != 12 { t.Fatal("old reader lost selection or marker") }
 if os.Getenv("REASONIX_COMPAT_DELETE_FLASH") == "1" {
  p.Models = slices.DeleteFunc(p.Models,func(m string) bool { return m == "mimo-v2.6-flash" })
 } else if !p.HasModel("mimo-v2.6-flash") { t.Fatal("old reader lost the new option") }
 p.DisplayName = "old-reader-edited"
 if err := c.SaveToScope(path,RenderScopeFull); err != nil { t.Fatal(err) }
}`
	if err := os.WriteFile(probe, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	// Overlay an existing test file: older Go versions do not discover a new
	// test filename supplied only by an overlay.
	data, err := json.Marshal(map[string]any{"Replace": map[string]string{filepath.Join(checkout, "internal/config/deepseek_catalog_downgrade_test.go"): probe}})
	if err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlay, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, deleted := range []string{"0", "1"} {
		cmd := exec.Command("go", "test", "-overlay", overlay, "./internal/config", "-run", "^TestMimoPreviousReaderProbe$", "-v", "-count=1")
		cmd.Dir = checkout
		cmd.Env = append(os.Environ(), "REASONIX_COMPAT_CONFIG="+path, "REASONIX_COMPAT_DELETE_FLASH="+deleted)
		output, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "--- PASS: TestMimoPreviousReaderProbe") {
			t.Fatalf("old reader: %v\n%s", err, output)
		}
		if changed, err := ApplyUserConfigUpgradesOnStartup(path); err != nil || changed {
			t.Fatalf("old writer lost migration marker: %v %v", changed, err)
		}
		c := LoadForEdit(path)
		p, ok := c.Provider("mimo")
		if !ok || c.ConfigVersion != Default().ConfigVersion || p.DisplayName != "old-reader-edited" ||
			p.DefaultModel() != "mimo-v2.5-pro" || p.HasModel("mimo-v2.6-flash") != (deleted == "0") ||
			p.Prices["mimo-v2.6-pro"] == nil || p.Prices["mimo-v2.6-pro"].Input != 3 {
			t.Fatalf("old reader round-trip lost choices: %+v", p)
		}
	}
}
