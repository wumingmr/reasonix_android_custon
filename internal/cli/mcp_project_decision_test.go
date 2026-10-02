package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/config"
)

// The CLI reads and records decisions for the workspace a session started in
// the same directory would use, and shows which project servers await one.
func TestMCPListAndEnableUseTheSessionWorkspace(t *testing.T) {
	isolateCLIConfigHome(t)
	repo := t.TempDir()
	sub := filepath.Join(repo, "pkg", "inner")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "reasonix.toml"), []byte("[[plugins]]\nname = \"repo-mcp\"\ncommand = \"repo-server\"\nargs = [\"--serve\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	list := func() string {
		return captureStdout(t, func() {
			if rc := Run([]string{"mcp", "list"}, "test-version"); rc != 0 {
				t.Fatalf("mcp list rc = %d", rc)
			}
		})
	}
	if out := list(); !strings.Contains(out, "repo-mcp") || !strings.Contains(out, "[awaiting_user_decision]") {
		t.Fatalf("mcp list from a subdirectory:\n%s", out)
	}
	out := captureStdout(t, func() {
		if rc := mcpEnableCLI([]string{"repo-mcp"}, true); rc != 0 {
			t.Fatalf("mcp enable rc = %d", rc)
		}
	})
	if !strings.Contains(out, "repo-server --serve") {
		t.Fatalf("mcp enable did not show what it approved:\n%s", out)
	}
	cfg, err := config.LoadForRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	if d := config.MCPServerDecision(cfg.Plugins[0], repo); d != config.MCPDecisionOn {
		t.Fatalf("decision at the session workspace = %s, want enabled", d.Code())
	}
	if out := list(); !strings.Contains(out, "[enabled]") {
		t.Fatalf("mcp list after enable:\n%s", out)
	}
}
