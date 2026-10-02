package main

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
)

func TestRemoveWorkspaceDropsVisibleTabsAndPersistedEntries(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectRoot := t.TempDir()
	if err := addProject(projectRoot, "Project"); err != nil {
		t.Fatalf("add project: %v", err)
	}
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"project": {ID: "project", Scope: "project", WorkspaceRoot: projectRoot, TopicID: "topic-project", Ready: true, disabledMCP: map[string]ServerView{}},
			"global":  {ID: "global", Scope: "global", WorkspaceRoot: globalTabWorkspaceRoot(), TopicID: "topic-global", Ready: true, disabledMCP: map[string]ServerView{}},
		},
		tabOrder:         []string{"project", "global"},
		activeTabID:      "project",
		detachedSessions: map[string]*WorkspaceTab{},
	}
	if err := app.workspaceRegistry().EnsureWorkspace(t.Context(), workspacestate.Workspace{
		ID: "preserved-owner", Root: projectRoot, Title: "Project", Visible: true,
	}); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.saveTabsLocked()
	app.mu.Unlock()

	if err := app.RemoveWorkspace(projectRoot); err != nil {
		t.Fatalf("RemoveWorkspace: %v", err)
	}
	assertTabIDs(t, app.ListTabs(), "global")
	if got := app.ListWorkspaces(); len(got) != 0 {
		t.Fatalf("workspaces after remove = %+v, want none", got)
	}
	if got := loadTabsFile(); len(got.Tabs) != 1 || got.Tabs[0].ID != "global" {
		t.Fatalf("persisted tabs after workspace remove = %+v, want only global", got)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Workspaces["preserved-owner"].Visible {
		t.Fatal("removed physical workspace remained visible in the authoritative registry")
	}
}

func TestRemoveWorkspaceKeepsProjectAndTabsWhenProjectFileWriteFails(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectRoot := t.TempDir()
	if err := addProject(projectRoot, "Project"); err != nil {
		t.Fatal(err)
	}
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"project": {ID: "project", Scope: "project", WorkspaceRoot: projectRoot, Ready: true, disabledMCP: map[string]ServerView{}},
			"global":  {ID: "global", Scope: "global", WorkspaceRoot: globalTabWorkspaceRoot(), Ready: true, disabledMCP: map[string]ServerView{}},
		},
		tabOrder: []string{"project", "global"}, activeTabID: "project",
		detachedSessions: map[string]*WorkspaceTab{},
	}
	if err := app.workspaceRegistry().EnsureWorkspace(t.Context(), workspacestate.Workspace{
		ID: "project-owner", Root: projectRoot, Title: "Project", Visible: true,
	}); err != nil {
		t.Fatal(err)
	}
	tmpPath := filepath.Join(desktopConfigDir(), desktopProjectsFile+".tmp")
	if err := os.Mkdir(tmpPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := app.RemoveWorkspace(projectRoot); err == nil {
		t.Fatal("RemoveWorkspace succeeded despite project file write failure")
	}
	assertTabIDs(t, app.ListTabs(), "project", "global")
	if got := app.ListWorkspaces(); len(got) != 1 || !sameProjectRoot(got[0].Path, projectRoot) {
		t.Fatalf("project file changed after failed removal: %+v", got)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Workspaces["project-owner"].Visible {
		t.Fatal("project was hidden after removal failed")
	}
	if err := os.Remove(tmpPath); err != nil {
		t.Fatal(err)
	}
	if err := app.RemoveWorkspace(projectRoot); err != nil {
		t.Fatalf("retry RemoveWorkspace: %v", err)
	}
}

func TestMergeCanonicalWorkspaceShellsUsesPersistedPhysicalOwner(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = t.Context()
	root := t.TempDir()

	if err := app.workspaceRegistry().EnsureWorkspace(t.Context(), workspacestate.Workspace{
		ID: "preserved-owner", Root: root, Title: "新建文件夹", Visible: true,
	}); err != nil {
		t.Fatal(err)
	}

	legacy := ProjectNode{Key: "project_" + root, Kind: "project", Root: root, Label: "Projects"}
	got := app.mergeCanonicalWorkspaceShells([]ProjectNode{legacy})
	if len(got) != 1 {
		t.Fatalf("one physical workspace rendered %d sidebar nodes: %+v", len(got), got)
	}
	if got[0].Key != legacy.Key || got[0].Label != legacy.Label {
		t.Fatalf("legacy presentation was replaced: got=%+v want=%+v", got[0], legacy)
	}
}

func TestMergeCanonicalWorkspaceShellsHonorsPersistedOwnerVisibility(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = t.Context()
	root := t.TempDir()

	if err := app.workspaceRegistry().EnsureWorkspace(t.Context(), workspacestate.Workspace{
		ID: "preserved-owner", Root: root, Title: "Hidden", Visible: false,
	}); err != nil {
		t.Fatal(err)
	}

	legacy := ProjectNode{Key: "project_" + root, Kind: "project", Root: root, Label: "Projects"}
	if got := app.mergeCanonicalWorkspaceShells([]ProjectNode{legacy}); len(got) != 0 {
		t.Fatalf("hidden physical workspace remained visible: %+v", got)
	}
}
