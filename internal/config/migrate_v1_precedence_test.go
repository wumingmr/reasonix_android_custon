package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateImportsLegacyV1TOMLBeforeJSON(t *testing.T) {
	srcJSON, dest, _ := legacyHome(t)
	legacyTOML := filepath.Join(filepath.Dir(dest), "reasonix.toml")
	if err := os.MkdirAll(filepath.Dir(legacyTOML), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyTOML, []byte(`
default_model = "deepseek-flash"
language = "en"

[ui]
theme = "light"
theme_style = "glacier"
close_behavior = "quit"

[[plugins]]
name = "legacy-v1"
command = "legacy-bin"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeLegacy(t, srcJSON, `{"apiKey":"sk-json-should-not-win"}`)

	res, err := MigrateLegacyIfNeeded()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if res == nil || res.From != legacyTOML {
		t.Fatalf("expected v1 TOML migration, got %+v", res)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	text := string(got)
	for _, want := range []string{fmt.Sprintf("config_version = %d", Default().ConfigVersion), `[desktop]`, `close_behavior = "quit"`, `name    = "legacy-v1"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("migrated TOML missing %q:\n%s", want, text)
		}
	}
	if _, err := os.Stat(UserCredentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("v1 TOML migration should not import lower-priority JSON key, credentials stat err=%v", err)
	}
}
