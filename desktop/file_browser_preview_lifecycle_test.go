package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"reasonix/internal/browser"
)

func filePreviewLifecycleFixture(t *testing.T) (*App, *WorkspaceTab, FileBrowserPreviewRequest) {
	t.Helper()
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html>preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	tab := &WorkspaceTab{ID: "preview", Scope: "project", WorkspaceRoot: root, SessionGeneration: 1}
	a.tabs["preview"] = tab
	a.tabOrder = []string{"preview"}
	t.Cleanup(a.stopWorkspacePreviewOrigin)
	return a, tab, FileBrowserPreviewRequest{Source: "workspace", Path: "index.html", OperationID: "preview"}
}

func assertPreviewRevoked(t *testing.T, rawURL string) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("retired preview returned %d", response.StatusCode)
	}
}

func TestFileBrowserPreviewTabRemovalDoesNotDeadlock(t *testing.T) {
	for _, operation := range []string{"remove", "transfer"} {
		t.Run(operation, func(t *testing.T) {
			a, tab, request := filePreviewLifecycleFixture(t)
			exec := &previewTestExecutor{tabs: map[string]browser.Tab{}}
			opened, err := a.openFileBrowserPreview(t.Context(), tab.ID, request, exec)
			if err != nil {
				t.Fatal(err)
			}
			a.mu.Lock()
			if operation == "remove" {
				a.removeTabOrderLocked(tab.ID)
			} else {
				applyRuntimeTab(&WorkspaceTab{ID: "replacement"}, tab, "", context.Background(), a)
			}
			a.mu.Unlock()
			if len(a.filePreviews.bindings) != 0 {
				t.Fatal("removed tab retained preview bindings")
			}
			a.ListTabs()
			a.Settings()
			assertPreviewRevoked(t, opened.URL)
		})
	}
}

type gatedFilePreviewExecutor struct {
	previewTestExecutor
	app     *App
	started chan string
	resume  chan struct{}
}

type cancellationAwarePreviewExecutor struct{ gatedFilePreviewExecutor }

func (e *cancellationAwarePreviewExecutor) Close(ctx context.Context, req browser.CloseRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return e.previewTestExecutor.Close(ctx, req)
}

func TestFileBrowserPreviewCancelledRequestClosesLateTab(t *testing.T) {
	a, tab, request := filePreviewLifecycleFixture(t)
	exec := &cancellationAwarePreviewExecutor{gatedFilePreviewExecutor{
		previewTestExecutor: previewTestExecutor{tabs: map[string]browser.Tab{}},
		app:                 a, started: make(chan string, 1), resume: make(chan struct{}),
	}}
	resume := sync.OnceFunc(func() { close(exec.resume) })
	defer resume()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := a.openFileBrowserPreview(ctx, tab.ID, request, exec); done <- err }()
	pendingURL := <-exec.started
	a.mu.Lock()
	a.removeTabOrderLocked(tab.ID)
	delete(a.tabs, tab.ID)
	a.mu.Unlock()
	cancel()
	resume()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("late preview attached to the removed tab")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled preview cleanup did not finish")
	}
	if len(exec.tabs) != 0 || len(exec.closes) != 1 {
		t.Fatalf("late browser tab was not closed: tabs=%d closes=%d", len(exec.tabs), len(exec.closes))
	}
	assertPreviewRevoked(t, pendingURL)
}

func (e *gatedFilePreviewExecutor) Open(ctx context.Context, req browser.OpenRequest) (browser.Tab, error) {
	e.wait(req.URL)
	return e.previewTestExecutor.Open(ctx, req)
}

func (e *gatedFilePreviewExecutor) Navigate(ctx context.Context, req browser.NavigateRequest) (browser.Tab, error) {
	e.wait(req.URL)
	return e.previewTestExecutor.Navigate(ctx, req)
}

func (e *gatedFilePreviewExecutor) wait(url string) {
	e.started <- url
	<-e.resume
	// A tool's browser executor resolves its current App binding before RPC.
	e.app.mu.RLock()
	e.app.mu.RUnlock() //nolint:staticcheck // Acquiring the lock itself simulates RPC binding resolution and detects lock inversion.
}

func TestFileBrowserPreviewRemovalDoesNotWaitForHostOrResurrectBinding(t *testing.T) {
	a, tab, request := filePreviewLifecycleFixture(t)
	exec := &gatedFilePreviewExecutor{previewTestExecutor: previewTestExecutor{tabs: map[string]browser.Tab{}}, app: a, started: make(chan string, 1), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(exec.resume) })
	defer resume()
	done := make(chan error, 1)
	go func() { _, err := a.openFileBrowserPreview(t.Context(), tab.ID, request, exec); done <- err }()
	url := <-exec.started
	removed := make(chan struct{})
	go func() {
		a.mu.Lock()
		a.removeTabOrderLocked(tab.ID)
		delete(a.tabs, tab.ID)
		a.mu.Unlock()
		close(removed)
	}()
	select {
	case <-removed:
	case <-time.After(3 * time.Second):
		t.Fatal("navigation waited for a pending browser RPC")
	}
	assertPreviewRevoked(t, url)
	a.Settings()
	resume()
	if err := <-done; err == nil {
		t.Fatal("late host reply republished a removed preview")
	}
	if len(a.filePreviews.bindings) != 0 || len(exec.tabs) != 0 {
		t.Fatal("late preview was not cleaned up")
	}
}

func TestFileBrowserPreviewRefreshRevocationFencesLateReply(t *testing.T) {
	for _, revoke := range []string{"browser-tab", "previous-url", "session-generation"} {
		t.Run(revoke, func(t *testing.T) {
			a, tab, request := filePreviewLifecycleFixture(t)
			exec := &gatedFilePreviewExecutor{previewTestExecutor: previewTestExecutor{tabs: map[string]browser.Tab{}}, app: a, started: make(chan string, 1), resume: make(chan struct{})}
			first, err := a.openFileBrowserPreview(t.Context(), tab.ID, request, &exec.previewTestExecutor)
			if err != nil {
				t.Fatal(err)
			}
			resume := sync.OnceFunc(func() { close(exec.resume) })
			defer resume()
			done := make(chan error, 1)
			request.OperationID = "refresh"
			go func() { _, err := a.openFileBrowserPreview(t.Context(), tab.ID, request, exec); done <- err }()
			pendingURL := <-exec.started
			switch revoke {
			case "browser-tab":
				a.releaseFileBrowserPreviewTab(first.TabID)
			case "previous-url":
				a.RevokeWorkspaceBrowserPreview(first.URL)
			case "session-generation":
				a.mu.Lock()
				tab.SessionGeneration++
				a.mu.Unlock()
			}
			if revoke != "session-generation" {
				assertPreviewRevoked(t, first.URL)
				assertPreviewRevoked(t, pendingURL)
			}
			resume()
			if err := <-done; err == nil {
				t.Fatal("refresh republished an explicitly revoked preview")
			}
			if len(a.filePreviews.bindings) != 0 || len(exec.tabs) != 0 {
				t.Fatal("revoked refresh retained a preview")
			}
			assertPreviewRevoked(t, first.URL)
			assertPreviewRevoked(t, pendingURL)
		})
	}
}

func TestFileBrowserPreviewLateReplyCannotAttachToReplacedSession(t *testing.T) {
	a, tab, request := filePreviewLifecycleFixture(t)
	exec := &gatedFilePreviewExecutor{previewTestExecutor: previewTestExecutor{tabs: map[string]browser.Tab{}}, app: a, started: make(chan string, 1), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(exec.resume) })
	defer resume()
	done := make(chan error, 1)
	go func() { _, err := a.openFileBrowserPreview(t.Context(), tab.ID, request, exec); done <- err }()
	pendingURL := <-exec.started
	a.mu.Lock()
	tab.SessionGeneration++
	a.mu.Unlock()
	resume()
	if err := <-done; err == nil {
		t.Fatal("old preview attached to the replacement session")
	}
	assertPreviewRevoked(t, pendingURL)
	if len(a.filePreviews.bindings) != 0 || len(exec.tabs) != 0 {
		t.Fatal("stale preview was not cleaned up")
	}
}
