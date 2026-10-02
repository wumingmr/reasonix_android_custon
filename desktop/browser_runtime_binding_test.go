package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"reasonix/internal/browser"
)

func TestBrowserExecutorFollowsRuntimeDetachAndReattach(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.hostShell = &hostShellBridge{app: a}
	source := &WorkspaceTab{ID: "source", SessionID: "session-a", Ready: true}
	source.sink = &tabEventSink{tabID: source.ID, app: a}
	a.tabs[source.ID] = source
	exec := a.browserExecutorForTab(source).(*tabBrowserExecutor)
	if !exec.Available(context.Background()) {
		t.Fatal("new task must have browser access")
	}

	if !a.detachRuntimeForReplacement(source) {
		t.Fatal("detach failed")
	}
	key := sessionRuntimeKey(sessionRoute(source.SessionID))
	detached := a.detachedSessions[key]
	if detached == nil {
		t.Fatal("detached runtime missing")
	}
	// The old visible surface now belongs to another session. Its browser
	// must never be used by the detached controller.
	a.mu.Lock()
	a.tabs[source.ID] = &WorkspaceTab{ID: source.ID, SessionID: "session-b"}
	a.mu.Unlock()
	assertBrowserBinding(t, a, exec, detached)
	detachedGrant, _ := exec.current()

	target := &WorkspaceTab{ID: "target"}
	a.mu.Lock()
	a.tabs[target.ID] = target
	delete(a.detachedSessions, key)
	applyRuntimeTab(target, detached, sessionRoute("session-a"), context.Background(), a)
	a.mu.Unlock()
	if !detachedGrant.revoked.Load() {
		t.Fatal("detached grant survived transfer to a new surface")
	}
	assertBrowserBinding(t, a, exec, target)

	a.setBrowserControlEnabled(false)
	if exec.Available(context.Background()) {
		t.Fatal("disabled browser control must still deny access")
	}
	a.setBrowserControlEnabled(true)
	assertBrowserBinding(t, a, exec, target)
	a.mu.Lock()
	delete(a.tabs, target.ID)
	a.mu.Unlock()
	if exec.Available(context.Background()) {
		t.Fatal("removed runtime must not retain browser access")
	}
}

func TestBrowserExecutorFollowsVisibleRuntimeTransfer(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.hostShell = &hostShellBridge{app: a}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><h1>Preview</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.stopWorkspacePreviewOrigin)
	source := &WorkspaceTab{ID: "source", SessionID: "session-a", Scope: "project", WorkspaceRoot: root}
	source.sink = &tabEventSink{tabID: source.ID, app: a}
	a.tabs[source.ID] = source
	exec := a.browserExecutorForTab(source).(*tabBrowserExecutor)
	target := &WorkspaceTab{ID: "target", Scope: "project", WorkspaceRoot: root}
	a.mu.Lock()
	delete(a.tabs, source.ID)
	a.tabs[target.ID] = target
	applyRuntimeTab(target, source, sessionRoute("session-a"), context.Background(), a)
	a.mu.Unlock()
	assertBrowserBinding(t, a, exec, target)
	current, _ := exec.current()
	current.host = &fakeBrowserHost{replies: map[string]any{
		"host/browser.tabs.open": map[string]any{"id": "preview-tab", "url": "https://example.test"},
	}}
	for _, tl := range browser.Tools(exec) {
		if tl.Name() == "browser_preview" {
			out, err := tl.Execute(context.Background(), []byte(`{"operationId":"preview-moved","source":"workspace","path":"index.html"}`))
			if err != nil || !strings.Contains(out, "preview-tab") {
				t.Fatalf("file preview after runtime transfer = %s, %v", out, err)
			}
		}
	}
}

func TestBrowserAvailabilityIsObservational(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.hostShell = &hostShellBridge{app: a}
	tab := &WorkspaceTab{ID: "task", SessionID: "session-a"}
	tab.sink = &tabEventSink{tabID: tab.ID, app: a}
	a.tabs[tab.ID] = tab
	exec := a.browserExecutorForTab(tab).(*tabBrowserExecutor)
	for range 3 {
		if !exec.Available(context.Background()) {
			t.Fatal("bound runtime unavailable")
		}
	}
	if len(a.browserExecutors) != 0 {
		t.Fatal("availability lookup minted a browser grant")
	}
}

func TestBrowserExecutorUsesReplacementRuntimeSink(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.hostShell = &hostShellBridge{app: a}
	tab := &WorkspaceTab{ID: "task", SessionID: "old"}
	tab.sink = &tabEventSink{tabID: tab.ID, app: a}
	a.tabs[tab.ID] = tab
	old := a.browserExecutorForTab(tab).(*tabBrowserExecutor)
	newSink := &tabEventSink{tabID: tab.ID, app: a}
	replacement := a.browserExecutorForRuntime(tab.ID, newSink).(*tabBrowserExecutor)
	if replacement.Available(context.Background()) {
		t.Fatal("unpublished replacement inherited the old runtime's browser")
	}
	a.mu.Lock()
	tab.sink = newSink
	tab.SessionID = "new"
	a.mu.Unlock()
	if old.Available(context.Background()) {
		t.Fatal("retired controller inherited the replacement runtime's browser")
	}
	if !replacement.Available(context.Background()) {
		t.Fatal("published replacement has no browser")
	}
}

func TestBrowserRuntimeCleanupPreservesReplacementGrant(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.hostShell = &hostShellBridge{app: a}
	old := &WorkspaceTab{ID: "task", SessionID: "old"}
	newTab := &WorkspaceTab{ID: old.ID, SessionID: "new"}
	a.tabs[newTab.ID] = newTab
	grant := a.hostBrowserExecutorForTab(newTab.ID)
	a.closeRemovedSessionRuntimes([]removedSessionRuntime{{tab: old}})
	if grant.revoked.Load() {
		t.Fatal("late cleanup revoked the replacement task's grant")
	}
	a.mu.Lock()
	delete(a.tabs, newTab.ID)
	a.mu.Unlock()
	a.closeRemovedSessionRuntimes([]removedSessionRuntime{{tab: newTab}})
	if !grant.revoked.Load() {
		t.Fatal("removed runtime retained its grant")
	}
}

func TestBrowserBindingConcurrentTransfer(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.hostShell = &hostShellBridge{app: a}
	source := &WorkspaceTab{ID: "source", SessionID: "session-a"}
	source.sink = &tabEventSink{tabID: source.ID, app: a}
	a.tabs[source.ID] = source
	exec := a.browserExecutorForTab(source).(*tabBrowserExecutor)
	target := &WorkspaceTab{ID: "target", SessionID: "session-a"}
	var readers sync.WaitGroup
	start := make(chan struct{})
	for range 4 {
		readers.Go(func() {
			<-start
			for range 100 {
				if !exec.Available(context.Background()) {
					t.Error("atomic runtime transfer exposed an unavailable binding")
					return
				}
				grant, err := exec.current()
				if err != nil || grant.sessionKey != "session-a:0" {
					t.Errorf("runtime transfer selected another task: grant=%v err=%v", grant, err)
					return
				}
			}
		})
	}
	close(start)
	for range 30 {
		a.mu.Lock()
		delete(a.tabs, source.ID)
		a.tabs[target.ID] = target
		applyRuntimeTab(target, source, sessionRoute("session-a"), context.Background(), a)
		a.mu.Unlock()
		source, target = target, source
	}
	readers.Wait()
}

func assertBrowserBinding(t *testing.T, a *App, proxy *tabBrowserExecutor, want *WorkspaceTab) {
	t.Helper()
	exec, err := proxy.current()
	if err != nil {
		t.Fatalf("current browser binding: %v", err)
	}
	if exec.tabID != want.ID || exec.browserSessionKey() != want.SessionID+":0" {
		t.Fatalf("browser bound to %q/%q, want %q/%q", exec.tabID, exec.browserSessionKey(), want.ID, want.SessionID)
	}
	// Exercise the real browser tool boundary, not just the boolean probe.
	host := &fakeBrowserHost{replies: map[string]any{"host/browser.tabs.list": map[string]any{"tabs": []map[string]any{{"id": "browser-tab"}}}}}
	exec.host = host
	for _, target := range browser.Tools(proxy) {
		if target.Name() == "browser_tabs" {
			if _, err := target.Execute(context.Background(), []byte(`{}`)); err != nil {
				t.Fatalf("browser tool failed after runtime move: %v", err)
			}
		}
	}
	if len(host.methods()) == 0 {
		t.Fatal("browser call did not reach the host")
	}
}
