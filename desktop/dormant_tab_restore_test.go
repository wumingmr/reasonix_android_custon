package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRemoteOnlyTabsFile persists a layout whose only surface is a remote
// shell: the state a single-surface remote user leaves behind on quit.
func writeRemoteOnlyTabsFile(t *testing.T) {
	t.Helper()
	dir := desktopConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"tabs":null,"activeTab":"r1","remoteTabs":[{"id":"r1","hostId":"box","workspace":"~/app"}],"remoteTabOrder":["r1"],"tabOrder":["r1"]}`
	if err := os.WriteFile(filepath.Join(dir, tabsFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A remote-only layout restores one dormant workspace tab so local commands
// have a target. It must satisfy the base ownership contract on that first
// build — a resolvable workspace identity — while never taking the visible
// surface from the remote shell it was restored beside.
func TestRemoteOnlyRestoreBuildsOwnedDormantTabWithoutTakingTheSurface(t *testing.T) {
	isolateDesktopUserDirs(t)
	seedBridgeTestHost(t, "box")
	writeRemoteOnlyTabsFile(t)
	a := NewApp()
	a.ctx = t.Context()
	a.readyHook = func() {}
	a.remoteRuntime = &fakeRemoteKernel{}
	t.Cleanup(func() { a.shutdown(context.Background()) })

	a.restoreOrBuildTabs()

	dormant, ctrl := a.activeOrSingleLocalTab()
	if dormant == nil {
		t.Fatal("remote-only restore left local commands without a target tab")
	}
	if ctrl != nil {
		t.Fatal("dormant tab built a runtime at startup")
	}
	// 8955c156f resolves ownership by workspace identity, so the tab must
	// carry one before its first build rather than deriving it later.
	if dormant.SessionWorkspace.ID == "" || dormant.SessionWorkspace.ID != desktopWorkspaceID("global", globalTabWorkspaceRoot()) {
		t.Fatalf("dormant workspace identity = %q, want the global workspace id", dormant.SessionWorkspace.ID)
	}
	if dormant.Scope != "global" || dormant.WorkspaceRoot != globalTabWorkspaceRoot() {
		t.Fatalf("dormant scope/root = %q/%q, want the global workspace", dormant.Scope, dormant.WorkspaceRoot)
	}
	a.mu.RLock()
	activeID, tabCount := a.activeTabID, len(a.tabs)
	a.mu.RUnlock()
	if activeID != "" || tabCount != 1 {
		t.Fatalf("restore activeTabID=%q localTabs=%d, want one inactive dormant tab", activeID, tabCount)
	}

	// The remote shell owns the visible surface; no fallback Global tab may
	// claim it, before or after the frontend activates the remote surface.
	a.remoteTabMu.Lock()
	a.remoteTabLayout.activeID = "r1"
	a.remoteTabMu.Unlock()
	for _, meta := range a.ListTabs() {
		if meta.Remote == nil && meta.Active {
			t.Fatalf("dormant local tab claimed the visible surface: %+v", meta)
		}
		if meta.Remote != nil && !meta.Active {
			t.Fatalf("remote surface lost the visible surface: %+v", meta)
		}
	}

	// Activating the dormant tab is the first demand for a runtime.
	if err := a.SetActiveTab(dormant.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for a.controllerForTab(dormant) == nil {
		if time.Now().After(deadline) {
			t.Fatal("activating the dormant tab never built a runtime")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The dormant tab is resolved before it owns a runtime, so a concurrent
// activation can publish one while the open is still waiting for the rebuild
// lock. Adopting the runtime the tab owns at that point — not the one read
// before the wait — keeps the open from failing with a spurious
// "tab runtime changed" error.
func TestOpenSessionAdoptsRuntimePublishedWhileWaitingForTheRebuildLock(t *testing.T) {
	app, tab, ctrl, _ := auditMigratedTab(t)
	ref, ok := ctrl.SessionRef()
	if !ok {
		t.Fatal("fixture controller has no session ref")
	}
	// Model the caller that resolved the tab while it was still dormant.
	app.mu.Lock()
	tab.Ctrl = nil
	app.mu.Unlock()

	// Holding turnStartMu parks the open after it takes runtimeRebuildMu, so
	// the publication below is ordered strictly after the resolve it races.
	tab.turnStartMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := app.resumeCanonicalSessionForTranscript(tab, nil, sessionRoute(ref.SessionID), defaultHistoryPageTurns, false)
		done <- err
	}()
	// The open owns the rebuild lock once TryLock fails; it is then parked on
	// turnStartMu, strictly after the resolve this publication races.
	deadline := time.Now().Add(20 * time.Second)
	for app.runtimeRebuildMu.TryLock() {
		app.runtimeRebuildMu.Unlock()
		if time.Now().After(deadline) {
			tab.turnStartMu.Unlock()
			t.Fatal("the open never reached the rebuild lock")
		}
		time.Sleep(time.Millisecond)
	}
	app.mu.Lock()
	tab.Ctrl = ctrl
	app.mu.Unlock()
	tab.turnStartMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("open adopted a stale nil runtime: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("open did not finish after the runtime was published")
	}
	if got := app.controllerForTab(tab); got != ctrl {
		t.Fatalf("tab controller after open = %p, want the published runtime %p", got, ctrl)
	}
}
