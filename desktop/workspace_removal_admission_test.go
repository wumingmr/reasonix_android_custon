package main

import (
	"testing"

	"reasonix/internal/control"
)

func TestRemoveWorkspaceBlocksNewProjectRuntimeAdmissionDuringSnapshot(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectRoot := t.TempDir()
	siblingRoot := t.TempDir()
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
	admitted, siblingAdmitted := false, false
	app.tabs["project"].Ctrl = &snapshotObservingSession{
		SessionAPI: control.New(control.Options{Label: "project"}),
		onSnapshot: func() {
			release, err := app.beginProjectRuntimeAdmission("project", projectRoot)
			if err == nil {
				admitted = true
				release()
			}
			release, err = app.beginProjectRuntimeAdmission("project", siblingRoot)
			if err == nil {
				siblingAdmitted = true
				release()
			}
		},
	}
	if err := app.RemoveWorkspace(projectRoot); err != nil {
		t.Fatal(err)
	}
	if admitted {
		t.Fatal("new project runtime was admitted while its workspace was being removed")
	}
	if !siblingAdmitted {
		t.Fatal("an unrelated project runtime was blocked by workspace removal")
	}
}
