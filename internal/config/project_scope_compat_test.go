package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestExistingProjectGrantsCoverDeclarationsAndDoNotResurrect(t *testing.T) {
	out := t.TempDir()
	rule := "Bash=echo exact $(value)"
	_, root := loadScoped(t, "", "[permissions]\nallow = "+renderStringArray([]string{rule})+"\n[sandbox]\nallow_write = ["+tomlQuote(filepath.Join(out, "child"))+"]\n")
	store := NewProjectGrantStore(ReasonixHomeDir())
	if err := store.Update(root, func(g ProjectGrant) (ProjectGrant, error) {
		g.Allow = []string{rule}
		g.AllowWrite = []string{out}
		return g, nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForRootReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.IgnoredProjectSettings()) != 0 || !slices.Equal(cfg.Permissions.Allow, []string{rule}) || !slices.Equal(cfg.Sandbox.AllowWrite, []string{out}) {
		t.Fatalf("effective=%+v diagnostics=%+v", cfg.Sandbox, cfg.IgnoredProjectSettings())
	}
	other := t.TempDir()
	otherCfg, err := LoadForRootReadOnly(other)
	if err != nil || len(otherCfg.Permissions.Allow) != 0 {
		t.Fatal("grant crossed workspace boundary", err)
	}
	if err := store.Update(root, func(g ProjectGrant) (ProjectGrant, error) { return ProjectGrant{}, nil }); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadForRootReadOnly(root)
	if err != nil || len(cfg.Permissions.Allow) != 0 || len(cfg.Sandbox.AllowWrite) != 0 || len(cfg.IgnoredProjectSettings()) != 2 {
		t.Fatalf("revoked grant restored: %v %+v", err, cfg.IgnoredProjectSettings())
	}
}

func TestEquivalentProjectRootUsesExistingUserValue(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	userPath := filepath.Clean(parent)
	projectPath := parent + string(filepath.Separator) + "."
	if runtime.GOOS == "windows" {
		projectPath = filepath.ToSlash(parent)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[sandbox]\nworkspace_root = "+tomlQuote(userPath)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte("[sandbox]\nworkspace_root = "+tomlQuote(projectPath)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForRootReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.IgnoredProjectSettings()) != 0 || cfg.Sandbox.WorkspaceRoot != userPath {
		t.Fatalf("root=%q diagnostics=%+v", cfg.Sandbox.WorkspaceRoot, cfg.IgnoredProjectSettings())
	}
}

func TestProjectPathIdentityAndBoundary(t *testing.T) {
	parent := t.TempDir()
	root, sibling := filepath.Join(parent, "repo"), filepath.Join(parent, "repo-other")
	for _, p := range []string{root, sibling} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	c := Default()
	if c.authorizedPath(root, []string{root}, sibling) {
		t.Fatal("string prefix granted sibling")
	}
	if !c.authorizedPath(root, []string{root}, filepath.Join(root, "missing", "child")) {
		t.Fatal("missing tail lost its authorized ancestor")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(sibling, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if c.authorizedPath(root, []string{root}, filepath.Join(link, "new")) {
		t.Fatal("external link granted by project root")
	}
	if !c.authorizedPath(root, []string{sibling}, filepath.Join(link, "new")) {
		t.Fatal("existing external grant not recognized")
	}
	upper := filepath.Join(parent, "REPO")
	if err := os.Mkdir(upper, 0700); err == nil {
		if c.equivalentConfigPath(root, root, upper) {
			t.Fatal("distinct case-sensitive directories conflated")
		}
	} else if os.IsExist(err) && !c.equivalentConfigPath(root, root, upper) {
		t.Fatal("case-insensitive directory identity lost")
	}
}

func TestProjectPermissionCoverageRetainsRuleSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, global, project string
		pending               bool
	}{
		{"exact", "Bash=echo one", "Bash=echo one", false},
		{"glob", "Bash(go test:*)", "Bash=go test ./...", false},
		{"tool", "Read", "Read(src/**)", false},
		{"no_broadening", "Bash=echo one", "Bash", true},
		{"literal_star", "Bash=echo *", "Bash=echo secret", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := loadScoped(t, "[permissions]\nmode='ask'\nallow="+renderStringArray([]string{tc.global})+"\ndeny=['Bash(rm:*)']\nask=['Write']\n", "[permissions]\nallow="+renderStringArray([]string{tc.project})+"\n")
			if len(cfg.IgnoredProjectSettings()) > 0 != tc.pending {
				t.Fatalf("diagnostics=%+v", cfg.IgnoredProjectSettings())
			}
			if !slices.Equal(cfg.Permissions.Allow, []string{tc.global}) || !slices.Contains(cfg.Permissions.Ask, "Write") || !slices.Contains(cfg.Permissions.Deny, "Bash(rm:*)") || cfg.Permissions.Mode != "ask" {
				t.Fatalf("permission semantics changed: %+v", cfg.Permissions)
			}
		})
	}
}

func TestExistingWorkspaceRootCoversExternalDeclaration(t *testing.T) {
	root := t.TempDir()
	cfg, _ := loadScoped(t, "[sandbox]\nworkspace_root="+tomlQuote(root)+"\n", "[sandbox]\nallow_write=["+tomlQuote(filepath.Join(root, "child"))+"]\n")
	if len(cfg.IgnoredProjectSettings()) != 0 || len(cfg.Sandbox.AllowWrite) != 0 {
		t.Fatalf("existing root not recognized: %+v", cfg.IgnoredProjectSettings())
	}
	// Declaring an unapproved workspace root must not bootstrap authorization.
	cfg, _ = loadScoped(t, "", "[sandbox]\nworkspace_root="+tomlQuote(root)+"\nallow_write=["+tomlQuote(filepath.Join(root, "child"))+"]\n")
	if len(cfg.IgnoredProjectSettings()) != 2 || len(cfg.Sandbox.AllowWrite) != 0 {
		t.Fatal("project root granted its own write declaration")
	}
}
func TestLegacyProjectDeclarationsAreNotGrantsOrLoadFailures(t *testing.T) {
	rules := make([]string, 39)
	for i := range rules {
		rules[i] = fmt.Sprintf("Bash=echo synthetic-command-%d", i)
	}
	project := "# preserved comment\n[model_roles]\nanswerer = 'unchanged'\n[permissions]\nallow = " + renderStringArray(rules) + "\n"
	cfg, root := loadScoped(t, "[permissions]\nmode='ask'\ndeny=['Bash(rm:*)']\n", project)
	file := filepath.Join(root, "reasonix.toml")
	before, _ := os.Stat(file)
	if cfg.HasLoadWarnings() || len(cfg.Permissions.Allow) != 0 || len(cfg.IgnoredProjectSettings()) != 39 {
		t.Fatalf("warnings=%v permissions=%+v ignored=%v", cfg.LoadWarnings(), cfg.Permissions, cfg.IgnoredProjectSettings())
	}
	for range 2 {
		if _, err := LoadForRootReadOnly(root); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := os.Stat(file)
	content, _ := os.ReadFile(file)
	if string(content) != project || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("read-only loading rewrote project file")
	}
}
func TestCorruptProjectGrantsRemainLoadWarnings(t *testing.T) {
	_, root := loadScoped(t, "", "")
	path := NewProjectGrantStore(ReasonixHomeDir()).Path()
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForRootReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HasLoadWarnings() || !strings.Contains(strings.Join(cfg.LoadWarnings(), "\n"), path) {
		t.Fatalf("warnings=%v", cfg.LoadWarnings())
	}
}
func TestProjectUnknownPresetWarningKeepsItsOrigin(t *testing.T) {
	cfg, root := loadScoped(t, "[desktop]\ndefault_tool_approval_mode='workspace-write'\n", "[desktop]\ndefault_tool_approval_mode='unknown-project-preset'\n")
	warnings := strings.Join(cfg.LoadWarnings(), "\n")
	if !strings.Contains(warnings, "project config sets desktop.default_tool_approval_mode") || !strings.Contains(warnings, "unknown-project-preset") || strings.Contains(warnings, filepath.Join(ReasonixHomeDir(), "config.toml")) {
		t.Fatalf("root=%s warnings=%s", root, warnings)
	}
}
