package main

import (
	"sync/atomic"
	"testing"
)

func TestSwitchWorkspaceMakesPreviouslyRemovedProjectVisibleAgain(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectRoot := t.TempDir()

	app := NewApp()
	installNoopRuntimeEvents(app)
	if _, err := app.SwitchWorkspace(projectRoot); err != nil {
		t.Fatalf("initial switch workspace: %v", err)
	}
	if err := app.RemoveWorkspace(projectRoot); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	var sidebarNotifications atomic.Int64
	app.projectTreeChangedHook = func() { sidebarNotifications.Add(1) }
	if _, err := app.SwitchWorkspace(projectRoot); err != nil {
		t.Fatalf("re-add workspace: %v", err)
	}
	if sidebarNotifications.Load() == 0 {
		t.Fatal("re-adding a hidden workspace did not notify the mounted project tree")
	}

	for _, project := range mustProjectTreeSnapshot(t, app).Projects {
		if project.Kind == "project" && sameProjectRoot(project.Root, projectRoot) {
			return
		}
	}
	t.Fatalf("re-added project %q is still hidden from the project tree", projectRoot)
}

func TestSwitchWorkspaceDoesNotRevealProjectDuringRemoval(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectRoot := t.TempDir()
	app := NewApp()
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().SetWorkspaceVisible(t.Context(), workspaceID, false); err != nil {
		t.Fatal(err)
	}
	release, err := app.reserveWorkspaceRemoval(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := app.SwitchWorkspace(projectRoot); err == nil {
		t.Fatal("SwitchWorkspace succeeded while the workspace was being removed")
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Workspaces[workspaceID].Visible {
		t.Fatal("failed re-add revealed a workspace that is still being removed")
	}
}
