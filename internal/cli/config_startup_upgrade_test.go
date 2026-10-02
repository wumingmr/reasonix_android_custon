package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/config"
)

func TestRunMigratesLegacyConfigBeforeConfigOnlyCommands(t *testing.T) {
	isolateCLIConfigHome(t)
	legacyPath := filepath.Join(filepath.Dir(config.UserConfigPath()), "reasonix.toml")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`
default_model = "deepseek-flash"

[[plugins]]
name = "legacy-cli"
command = "legacy-bin"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := Run([]string{"mcp", "list"}, "test-version"); rc != 0 {
			t.Fatalf("mcp list rc = %d, want 0", rc)
		}
	})
	if !strings.Contains(out, "legacy-cli") {
		t.Fatalf("mcp list should include migrated legacy config:\n%s", out)
	}

	body, err := os.ReadFile(config.UserConfigPath())
	if err != nil {
		t.Fatalf("read migrated user config: %v", err)
	}
	for _, want := range []string{fmt.Sprintf("config_version = %d", config.Default().ConfigVersion), `[desktop]`, `name    = "legacy-cli"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("migrated config missing %q:\n%s", want, body)
		}
	}
}

func TestRunAppliesUserConfigUpgradesOnStartup(t *testing.T) {
	isolateCLIConfigHome(t)
	path := config.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("config_version = 2\ndefault_model = \"deepseek-flash\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	captureStdout(t, func() {
		if rc := Run([]string{"mcp", "list"}, "test-version"); rc != 0 {
			t.Fatalf("mcp list rc = %d, want 0", rc)
		}
	})

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read upgraded user config: %v", err)
	}
	if !strings.Contains(string(body), fmt.Sprintf("config_version = %d", config.Default().ConfigVersion)) {
		t.Fatalf("CLI startup should apply user config upgrades:\n%s", body)
	}
}
