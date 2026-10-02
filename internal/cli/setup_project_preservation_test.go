package cli

import (
	"os"
	"path/filepath"
	"reasonix/internal/config"
	"strings"
	"testing"
)

func TestProjectShellSetupPreservesUnknownFields(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "reasonix.toml")
	body := "future_setting='keep-top'\n[[providers]]\nname='relay'\nkind='openai'\nbase_url='https://relay.invalid/v1'\nmodel='chat'\napi_key_env='OLD_KEY'\nfuture_option='keep-provider'\n[providers.future_table]\nvalue='keep-nested'\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	s := newProviderSetupSessionForPath(cfg, path)
	if err := s.setCredentialForProviders([]string{"relay"}, "OLD_KEY", "fixture-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"keep-top", "keep-provider", "keep-nested"} {
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("lost %s", marker)
		}
	}
	if _, err := os.Stat(config.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatal("project provider was promoted to user config")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), ".env")); !os.IsNotExist(err) {
		t.Fatal("credential saved into project")
	}
}
