package main

import (
	"os"
	"testing"
)

func TestWorkspaceShellSnapshotDoesNotFallbackPastRegistryFailure(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	root := t.TempDir()
	id, err := a.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	if err := addProject(root, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.workspaceRegistry().SetWorkspaceVisible(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.workspaceRegistry().Path(), []byte("broken registry"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GetProjectTreeSnapshot(); err == nil {
		t.Fatal("registry failure must not publish legacy-only membership")
	}
}

func mustProjectTreeSnapshot(t *testing.T, a *App) ProjectTreeSnapshot {
	t.Helper()
	snapshot, err := a.GetProjectTreeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestWorkspaceShellSnapshotUsesMembershipGeneration(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	root := t.TempDir()
	id, err := a.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	before := mustProjectTreeSnapshot(t, a)
	if before.WorkspaceGeneration == nil {
		t.Fatal("missing membership generation")
	}
	if err := a.workspaceRegistry().SetWorkspaceVisible(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}
	after := mustProjectTreeSnapshot(t, a)
	if after.WorkspaceGeneration == nil || *after.WorkspaceGeneration <= *before.WorkspaceGeneration {
		t.Fatal("visibility mutation did not advance membership generation")
	}
	for _, project := range after.Projects {
		if sameProjectRoot(project.Root, root) {
			t.Fatal("generation and membership came from different states")
		}
	}
}
