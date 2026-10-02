package main

import "testing"

func TestRemoteContextPanelReadsServeContext(t *testing.T) {
	fs := newFakeServe(t, "s3cret", nil)
	fs.newSessionPath = "/sessions/remote.jsonl"
	kernel := &fakeRemoteKernel{
		statuses:    []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView:  RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL},
		ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	meta, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{NewSession: true})
	if err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, a, meta.ID, "ready")
	if got := a.ContextPanel(meta.ID); got.UsedTokens != 10 || got.WindowTokens != 128 {
		t.Fatalf("remote panel context = %d/%d, want Serve /context value 10/128", got.UsedTokens, got.WindowTokens)
	}
	if got := a.ContextUsageForTab(meta.ID); got.Used != 10 || got.Window != 128 {
		t.Fatalf("remote usage context = %d/%d, want 10/128", got.Used, got.Window)
	}
	fs.mu.Lock()
	expected := fs.contextExpected
	fs.mu.Unlock()
	a.remoteTabMu.Lock()
	selected := a.remoteTabs[meta.ID].routing.currentPath
	a.remoteTabMu.Unlock()
	if expected != selected {
		t.Fatalf("remote context request targeted %q, want selected %q", expected, selected)
	}
	a.remoteTabMu.Lock()
	a.remoteTabs[meta.ID].routing.currentPath = remoteSessionIDRoutePrefix + "canonical-a"
	a.remoteTabs[meta.ID].routing.pathRevision++
	a.remoteTabMu.Unlock()
	if got := a.ContextPanel(meta.ID); got.UsedTokens != 10 {
		t.Fatalf("canonical remote panel used tokens = %d, want 10", got.UsedTokens)
	}
	fs.mu.Lock()
	expected = fs.contextExpected
	fs.mu.Unlock()
	if expected != remoteSessionIDRoutePrefix+"canonical-a" {
		t.Fatalf("canonical context header = %q", expected)
	}
	started, release := make(chan struct{}, 1), make(chan struct{})
	fs.mu.Lock()
	fs.contextStarted, fs.contextRelease = started, release
	fs.mu.Unlock()
	done := make(chan ContextPanelInfo, 1)
	go func() { done <- a.ContextPanel(meta.ID) }()
	<-started
	a.remoteTabMu.Lock()
	a.remoteTabs[meta.ID].routing.currentPath = remoteSessionIDRoutePrefix + "canonical-b"
	a.remoteTabs[meta.ID].routing.pathRevision++
	a.remoteTabMu.Unlock()
	close(release)
	if got := <-done; got.UsedTokens != 0 || got.WindowTokens != 0 {
		t.Fatalf("stale remote context reached the switched tab: %+v", got)
	}
}
