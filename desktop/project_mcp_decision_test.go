package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/plugin"
)

// enableProjectMCPForTest records the user's enable decision for each server
// root's project files declare with auto_start on.
func enableProjectMCPForTest(t testing.TB, root string) {
	t.Helper()
	enableProjectMCPForWorkspace(t, root, root)
}

// enableProjectMCPForWorkspace records the decision under workspace, which the
// test app's global tab leaves empty while config still loads from root.
func enableProjectMCPForWorkspace(t testing.TB, root, workspace string) {
	t.Helper()
	cfg, err := config.LoadForRootReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cfg.Plugins {
		if p.Source.ProjectScoped() && p.ShouldAutoStart() {
			if err := config.DefaultMCPActivationStore().SetServerEnabled(p, workspace, true); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Project configuration cannot choose MCP servers the desktop starts: without
// the user's decision the shared host never connects to one.
func TestDesktopSharedHostProjectMCPWaitsForTheUser(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	srv := desktopMCPHTTPServer(t)
	defer srv.Close()
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), fmt.Appendf(nil, `
[[plugins]]
name = "h"
type = "http"
url = %q
`, srv.URL), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sharedHost := plugin.NewHost()
	defer sharedHost.Close()
	ctrl, err := boot.Build(ctx, boot.Options{
		WorkspaceRoot: dir,
		SessionDir:    filepath.Join(dir, "sessions"),
		SharedHost:    sharedHost,
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatalf("boot.Build: %v", err)
	}
	defer ctrl.Close()

	time.Sleep(time.Second)
	if sharedHost.HasClient("h") {
		t.Fatal("project MCP connected with no user decision")
	}
}

func TestCapabilitiesShowsDefaultMCPAsAutomaticIdleNotDisabled(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "playwright"
command = "npx"
args = ["-y", "@playwright/mcp"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	enableProjectMCPForWorkspace(t, dir, "")

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "playwright" {
			if s.Status != "deferred" || s.StartIntent != "automatic" || s.RuntimeState != "idle" {
				t.Fatalf("default MCP view = %+v, want deferred automatic idle", s)
			}
			return
		}
	}
	t.Fatalf("playwright MCP missing from Capabilities: %+v", view.Servers)
}
