package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/tool"
)

func TestAddPermissionRuleRejectsBareShellCommandBeforeSaving(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	if err := (&App{}).AddPermissionRule("deny", "rm"); err == nil || !strings.Contains(err.Error(), "Bash(rm:*)") {
		t.Fatalf("AddPermissionRule(deny, rm) = %v, want a Bash rule suggestion", err)
	}
	if _, err := os.Stat(config.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("rejected rule wrote config: stat error = %v", err)
	}
	cfg := config.Default()
	cfg.Permissions.Deny = []string{"rm"} // Existing entries must not be rewritten.
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save existing config: %v", err)
	}
	before, err := os.ReadFile(config.UserConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).AddPermissionRule("deny", "git reset"); err == nil {
		t.Fatal("bare command was accepted into existing config")
	}
	after, err := os.ReadFile(config.UserConfigPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected rule changed existing config: read error = %v", err)
	}
}

func TestValidateSavedPermissionRuleUsesRegisteredTools(t *testing.T) {
	registered := []tool.ContractEntry{{Name: "bash"}, {Name: "write_file"}, {Name: "plugin_custom"}}
	for _, tc := range []struct {
		list, rule, suggestion string
	}{
		{"deny", "rm", "Bash(rm:*)"},
		{"ask", "git reset", "Bash(git reset:*)"},
		{"allow", "git branch", "Bash(git branch)"},
	} {
		err := validateSavedPermissionRule(tc.list, tc.rule, registered, nil)
		if err == nil || !strings.Contains(err.Error(), tc.suggestion) {
			t.Errorf("%s %q: got %v, want %q suggestion", tc.list, tc.rule, err, tc.suggestion)
		}
	}
	for _, rule := range []string{"Bash(rm:*)", "Edit(src/**)", "plugin_custom"} {
		if err := validateSavedPermissionRule("deny", rule, registered, nil); err != nil {
			t.Errorf("registered rule %q: %v", rule, err)
		}
	}
	if err := validateSavedPermissionRule("deny", "Bash(rm:*)", tool.BuiltinContractEntries(), nil); err != nil {
		t.Errorf("desktop built-in Bash rule: %v", err)
	}
}

func TestAddPermissionRuleAcceptsConfiguredDisconnectedMCPServer(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Plugins = []config.PluginEntry{{Name: "github", Type: "http", URL: "https://example.invalid/mcp"}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}
	app := &App{} // No active session or connected MCP registry.
	for _, rule := range []string{"mcp__github__create_issue", "mcp_connect__github"} {
		if err := app.AddPermissionRule("deny", rule); err != nil {
			t.Fatalf("configured disconnected MCP rule %q: %v", rule, err)
		}
	}
	if err := app.AddPermissionRule("deny", "mcp__other__create_issue"); err == nil {
		t.Fatal("accepted an unconfigured MCP server")
	}
	if err := app.AddPermissionRule("deny", "mcp__github__*"); err == nil || !strings.Contains(err.Error(), "exact tool name") {
		t.Fatalf("MCP tool-name glob = %v, want exact-name guidance", err)
	}
	got, err := config.LoadForEditReadOnlyStrict(config.UserConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Permissions.Deny) != 2 || got.Permissions.Deny[0] != "mcp__github__create_issue" || got.Permissions.Deny[1] != "mcp_connect__github" {
		t.Fatalf("saved MCP deny rules = %q", got.Permissions.Deny)
	}
}
