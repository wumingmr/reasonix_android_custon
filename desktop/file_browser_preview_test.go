package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/browser"
)

type previewTestExecutor struct {
	tabs      map[string]browser.Tab
	opens     []browser.OpenRequest
	navs      []browser.NavigateRequest
	closes    []browser.CloseRequest
	userNavs  []browser.NavigateRequest
	takenOver bool
}

func (e *previewTestExecutor) Tabs(context.Context) ([]browser.Tab, error) {
	out := make([]browser.Tab, 0, len(e.tabs))
	for _, tab := range e.tabs {
		out = append(out, tab)
	}
	return out, nil
}
func (e *previewTestExecutor) Open(_ context.Context, req browser.OpenRequest) (browser.Tab, error) {
	e.opens = append(e.opens, req)
	tab := browser.Tab{ID: "browser-" + req.OperationID, URL: req.URL}
	e.tabs[tab.ID] = tab
	return tab, nil
}
func (e *previewTestExecutor) Navigate(_ context.Context, req browser.NavigateRequest) (browser.Tab, error) {
	if e.takenOver {
		return browser.Tab{}, browser.ErrTakenOver
	}
	e.navs = append(e.navs, req)
	tab := e.tabs[req.TabID]
	tab.URL = req.URL
	e.tabs[req.TabID] = tab
	return tab, nil
}
func (e *previewTestExecutor) navigateFilePreview(_ context.Context, req browser.NavigateRequest) (browser.Tab, error) {
	e.userNavs = append(e.userNavs, req)
	tab := e.tabs[req.TabID]
	tab.URL = req.URL
	e.tabs[req.TabID] = tab
	return tab, nil
}
func (e *previewTestExecutor) Close(_ context.Context, req browser.CloseRequest) error {
	e.closes = append(e.closes, req)
	delete(e.tabs, req.TabID)
	return nil
}
func (*previewTestExecutor) Snapshot(context.Context, browser.SnapshotRequest) (browser.Snapshot, error) {
	return browser.Snapshot{}, browser.ErrNoGrant
}
func (*previewTestExecutor) Screenshot(context.Context, browser.ScreenshotRequest) (browser.Screenshot, error) {
	return browser.Screenshot{}, browser.ErrNoGrant
}
func (*previewTestExecutor) Act(context.Context, browser.ActRequest) (browser.ActResult, error) {
	return browser.ActResult{}, browser.ErrNoGrant
}
func (*previewTestExecutor) Downloads(context.Context, browser.DownloadsRequest) ([]browser.Download, error) {
	return nil, browser.ErrNoGrant
}

func TestFileBrowserPreviewReusesTaskTabAndHonoursGeneration(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><button>first</button>"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.tabs["task"] = &WorkspaceTab{ID: "task", Scope: "project", WorkspaceRoot: root, SessionGeneration: 7}
	t.Cleanup(app.stopWorkspacePreviewOrigin)
	exec := &previewTestExecutor{tabs: map[string]browser.Tab{}}

	first, err := app.openFileBrowserPreview(context.Background(), "task", FileBrowserPreviewRequest{
		Source: "workspace", Path: "index.html", OperationID: "open-1", ExpectedSessionGeneration: 7,
	}, exec)
	if err != nil || first.TabID == "" || len(exec.opens) != 1 || len(exec.navs) != 0 {
		t.Fatalf("first preview = %+v err=%v opens=%d navs=%d", first, err, len(exec.opens), len(exec.navs))
	}
	second, err := app.openFileBrowserPreview(context.Background(), "task", FileBrowserPreviewRequest{
		Source: "workspace", Path: "index.html", OperationID: "open-2", ExpectedSessionGeneration: 7,
	}, exec)
	if err != nil || second.TabID != first.TabID || len(exec.opens) != 1 || len(exec.navs) != 1 {
		t.Fatalf("second preview = %+v err=%v opens=%d navs=%d", second, err, len(exec.opens), len(exec.navs))
	}

	exec.takenOver = true
	if _, err := app.openFileBrowserPreview(context.Background(), "task", FileBrowserPreviewRequest{
		Source: "workspace", Path: "index.html", OperationID: "open-3", ExpectedSessionGeneration: 7,
	}, exec); !errors.Is(err, browser.ErrTakenOver) {
		t.Fatalf("taken-over refresh error = %v, want %v", err, browser.ErrTakenOver)
	}
	if len(exec.opens) != 1 {
		t.Fatalf("taken-over refresh opened a bypass tab: %d opens", len(exec.opens))
	}
	userRefresh, err := app.openFileBrowserPreview(context.Background(), "task", FileBrowserPreviewRequest{
		Source: "workspace", Path: "index.html", OperationID: "user-refresh", ExpectedSessionGeneration: 7, UserInitiated: true,
	}, exec)
	if err != nil || userRefresh.TabID != first.TabID || len(exec.userNavs) != 1 || len(exec.opens) != 1 {
		t.Fatalf("explicit user refresh = %+v err=%v userNavs=%d opens=%d", userRefresh, err, len(exec.userNavs), len(exec.opens))
	}

	if _, err := app.openFileBrowserPreview(context.Background(), "task", FileBrowserPreviewRequest{
		Source: "workspace", Path: "index.html", OperationID: "stale", ExpectedSessionGeneration: 6,
	}, exec); err == nil {
		t.Fatal("stale session generation opened a preview")
	}
}

func TestFileBrowserPreviewDoesNotOverwriteAUserNavigation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.tabs["task"] = &WorkspaceTab{ID: "task", Scope: "project", WorkspaceRoot: root, SessionGeneration: 1}
	t.Cleanup(app.stopWorkspacePreviewOrigin)
	exec := &previewTestExecutor{tabs: map[string]browser.Tab{}}
	first, err := app.openFileBrowserPreview(context.Background(), "task", FileBrowserPreviewRequest{Source: "workspace", Path: "index.html", OperationID: "one"}, exec)
	if err != nil {
		t.Fatal(err)
	}
	tab := exec.tabs[first.TabID]
	tab.URL = "https://example.test/user-page"
	exec.tabs[first.TabID] = tab
	second, err := app.openFileBrowserPreview(context.Background(), "task", FileBrowserPreviewRequest{Source: "workspace", Path: "index.html", OperationID: "two"}, exec)
	if err != nil {
		t.Fatal(err)
	}
	if second.TabID == first.TabID || len(exec.opens) != 2 || len(exec.navs) != 0 {
		t.Fatalf("user navigation overwritten: first=%s second=%s opens=%d navs=%d", first.TabID, second.TabID, len(exec.opens), len(exec.navs))
	}
}

func TestFileBrowserPreviewExplicitRevokeReleasesBinding(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.tabs["task"] = &WorkspaceTab{ID: "task", Scope: "project", WorkspaceRoot: root, SessionGeneration: 1}
	t.Cleanup(app.stopWorkspacePreviewOrigin)
	exec := &previewTestExecutor{tabs: map[string]browser.Tab{}}
	opened, err := app.openFileBrowserPreview(context.Background(), "task", FileBrowserPreviewRequest{
		Source: "workspace", Path: "index.html", OperationID: "open",
	}, exec)
	if err != nil {
		t.Fatal(err)
	}
	app.RevokeWorkspaceBrowserPreview(opened.URL)
	if len(app.filePreviews.bindings) != 0 {
		t.Fatalf("preview registry retained %d bindings after explicit close revoke", len(app.filePreviews.bindings))
	}
}
