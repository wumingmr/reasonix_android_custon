package main

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
)

func TestKeepOnlyVisibleTabKeepsCanonicalRuntimeWhenRegistryCannotPersist(t *testing.T) {
	isolateDesktopUserDirs(t)
	statePath := filepath.Join(t.TempDir(), "desktop", "workspace-state-v1.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hidden := &WorkspaceTab{
		ID: "hidden", Scope: "global", WorkspaceRoot: globalTabWorkspaceRoot(),
		SessionID: "durable-session", Ready: true, disabledMCP: map[string]ServerView{},
	}
	target := &WorkspaceTab{ID: "target", Scope: "global", Ready: true, disabledMCP: map[string]ServerView{}}
	app := &App{
		tabs: map[string]*WorkspaceTab{"hidden": hidden, "target": target}, tabOrder: []string{"hidden", "target"},
		activeTabID: "hidden",
		desktopPersistenceState: desktopPersistenceState{
			desktopSessions: desktopSessionState{workspaceState: workspacestate.NewStore(statePath)},
		},
	}
	if _, err := app.keepOnlyVisibleTab("target"); err == nil {
		t.Fatal("keepOnlyVisibleTab succeeded despite an unpublishable canonical registry")
	}
	if app.tabs["hidden"] != hidden || hidden.removed {
		t.Fatal("canonical tab was pruned after registry persistence failed")
	}
}
