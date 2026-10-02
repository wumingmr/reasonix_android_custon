package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/config"
)

func TestDoctorPreservesProjectSandboxHintAndDeclarationValues(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	t.Chdir(root)
	project := "[sandbox]\nnetwork=false\n[permissions]\nallow=['Bash=echo synthetic-rule']\n"
	if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte(project), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadForRootReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HasLoadWarnings() {
		t.Fatalf("ordinary declaration produced a banner warning: %v", cfg.LoadWarnings())
	}
	text := RenderText(Collect(Options{Config: cfg}))
	for _, want := range []string{"sets [sandbox]", "narrow user-level Settings", "permissions.allow", "Bash=echo synthetic-rule", "approval is required"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
}

func TestDoctorPreservesUnknownPresetKeyValueAndProjectOrigin(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte("[desktop]\ndefault_tool_approval_mode='unknown-project-preset'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadForRootReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	text := RenderText(Collect(Options{Config: cfg}))
	if !strings.Contains(text, "project config sets desktop.default_tool_approval_mode") || !strings.Contains(text, "unknown-project-preset") {
		t.Fatalf("warning lost project origin or key/value: %s", text)
	}
}
