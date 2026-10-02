package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadWithStatusline(t *testing.T, user, project string) *Config {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	if user != "" {
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[statusline]\ncommand = \""+user+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte("[statusline]\ncommand = \""+project+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestProjectStatuslineIsIgnored(t *testing.T) {
	if got := loadWithStatusline(t, "", "project-statusline").Statusline.Command; got != "" {
		t.Fatalf("statusline command = %q, want none from a project file", got)
	}
}

func TestUserStatuslineWinsOverProject(t *testing.T) {
	if got := loadWithStatusline(t, "user-statusline", "project-statusline").Statusline.Command; got != "user-statusline" {
		t.Fatalf("statusline command = %q, want the user value", got)
	}
}

func TestStatuslineNeverRendersIntoAProjectFile(t *testing.T) {
	cfg := Default()
	cfg.Statusline.Command = "user-statusline"
	if out := RenderTOMLForScope(cfg, RenderScopeProject); strings.Contains(out, "user-statusline") {
		t.Fatalf("project render carries the statusline command:\n%s", out)
	}
	if out := RenderTOMLProjectDelta(cfg); strings.Contains(out, "user-statusline") {
		t.Fatalf("project delta carries the statusline command:\n%s", out)
	}
	if out := RenderTOMLForScope(cfg, RenderScopeUser); !strings.Contains(out, "user-statusline") {
		t.Fatalf("user render lost the statusline command:\n%s", out)
	}
}
