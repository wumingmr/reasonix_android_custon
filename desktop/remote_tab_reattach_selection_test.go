package main

import (
	"testing"
)

func reattachSelectionFixture(t *testing.T, sessions []serveSessionEntry) (*fakeServe, *App) {
	t.Helper()
	fs := newFakeServe(t, "s3cret", sessions)
	kernel := &fakeRemoteKernel{
		statuses:   []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView: RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL, InstanceID: "serve-1"}, ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	previousDelays := remoteTabReattachDelays
	remoteTabReattachDelays = nil
	t.Cleanup(func() { remoteTabReattachDelays = previousDelays })
	return fs, a
}

// dropRemoteTabPump retires the live pump the way a dead stream does, leaving
// the tab reconnecting on the same Serve instance.
func dropRemoteTabPump(a *App, tabID string) {
	a.remoteTabMu.Lock()
	defer a.remoteTabMu.Unlock()
	tab := a.remoteTabs[tabID]
	if tab.cancel != nil {
		tab.cancel()
	}
	tab.gen++
	tab.cancel, tab.client, tab.base, tab.token = nil, nil, "", ""
	tab.state = "reconnecting"
}

// A reattach on a surviving Serve must land on the session the tab was opened
// for. Another client can move Serve's foreground while the stream is down;
// publishing ready without re-entering would let the next /status adopt that
// foreign session and silently swap the user's conversation.
func TestRemoteTabReattachReentersSelectedSessionWhenServeMoved(t *testing.T) {
	const selected = "/sessions/s2.jsonl"
	fs, a := reattachSelectionFixture(t, []serveSessionEntry{
		{Name: "s1", Path: "/sessions/s1.jsonl", Current: true},
		{Name: "s2", Path: selected},
	})
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{SessionName: "s2", SessionPath: selected})

	fs.mu.Lock()
	for i := range fs.sessions {
		fs.sessions[i].Current = fs.sessions[i].Name == "s1"
	}
	fs.resumePath = ""
	fs.mu.Unlock()
	dropRemoteTabPump(a, meta.ID)

	if !a.reattachRemoteTabOnce(meta.ID) {
		t.Fatal("reattach on the surviving serve failed")
	}
	_, resumed, _ := fs.snapshot()
	if resumed != selected {
		t.Fatalf("reattach resumed %q, want the tab's selected session %q re-entered", resumed, selected)
	}
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[meta.ID]
	state, route, name := tab.state, tab.routing.currentPath, tab.session.name
	a.remoteTabMu.Unlock()
	if state != "ready" || route != selected || name != "s2" {
		t.Fatalf("reattached state/route/name = %q/%q/%q, want ready on %q", state, route, name, selected)
	}
	sessions, err := a.RemoteProjectSessions("box", "~/app")
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		if session.Current && session.Path != selected {
			t.Fatalf("serve foreground after reattach = %+v, want %q", session, selected)
		}
	}
}

// When Serve still runs the selected session the reattach only rebuilds the
// stream: no redundant /resume transition is issued.
func TestRemoteTabReattachKeepsStreamWhenServeStillRunsSelection(t *testing.T) {
	const selected = "/sessions/s2.jsonl"
	fs, a := reattachSelectionFixture(t, []serveSessionEntry{
		{Name: "s1", Path: "/sessions/s1.jsonl", Current: true},
		{Name: "s2", Path: selected},
	})
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{SessionName: "s2", SessionPath: selected})
	fs.mu.Lock()
	fs.resumePath = ""
	fs.mu.Unlock()
	dropRemoteTabPump(a, meta.ID)

	if !a.reattachRemoteTabOnce(meta.ID) {
		t.Fatal("reattach on the surviving serve failed")
	}
	if _, resumed, _ := fs.snapshot(); resumed != "" {
		t.Fatalf("reattach re-entered %q although serve still ran the selection", resumed)
	}
	a.remoteTabMu.Lock()
	state, route := a.remoteTabs[meta.ID].state, a.remoteTabs[meta.ID].routing.currentPath
	a.remoteTabMu.Unlock()
	if state != "ready" || route != selected {
		t.Fatalf("reattached state/route = %q/%q, want ready on %q", state, route, selected)
	}
}

// A first-open New Topic whose /events stream is refused never sent /new. The
// reattach loop that recovers it must still create the requested blank instead
// of publishing ready on whatever session Serve happens to run.
func TestRemoteTabFirstOpenReattachEntersRequestedNewSession(t *testing.T) {
	const fresh = "/sessions/fresh.jsonl"
	fs, a := reattachSelectionFixture(t, []serveSessionEntry{{Name: "s1", Path: "/sessions/s1.jsonl", Current: true}})
	fs.mu.Lock()
	fs.eventsFailCount = 1
	fs.newSessionPath = fresh
	fs.mu.Unlock()

	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})
	newCalled, _, _ := fs.snapshot()
	if newCalled != 1 {
		t.Fatalf("POST /new called %d times, want exactly one from the recovering reattach", newCalled)
	}
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[meta.ID]
	route, reset, name := tab.routing.currentPath, tab.session.reset, tab.session.name
	a.remoteTabMu.Unlock()
	if route != fresh || !reset || name != "" {
		t.Fatalf("recovered New Topic route/reset/name = %q/%v/%q, want the fresh blank %q", route, reset, name, fresh)
	}
	if err := a.SubmitRemoteTab(meta.ID, "hi"); err != nil {
		t.Fatalf("submit after recovered first open: %v", err)
	}
	if fenced := fs.recordedExpectedPaths(); len(fenced) == 0 || fenced[len(fenced)-1] != fresh {
		t.Fatalf("submit fenced against %v, want the recovered blank %q", fenced, fresh)
	}
}
