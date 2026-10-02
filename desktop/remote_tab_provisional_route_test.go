package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Retiring a pump generation must close the provisional route epoch: every
// commit and rollback of the in-flight /resume fences on that generation, so
// nothing else could ever clear the gate and the tab would refuse commands
// ("switching sessions") and buffer live frames forever.
func TestRetiringPumpGenerationClosesProvisionalRouteEpoch(t *testing.T) {
	newTab := func() (*App, *remoteTab) {
		tab := &remoteTab{
			id: "remote-1", ref: RemoteTabRef{HostID: "box", Workspace: "~/app"}, state: "ready", gen: 4,
			routing: remoteTabSessionRouting{
				currentPath: "session-id:target", rehydratingPath: "session-id:target",
				rehydratingFrames: []json.RawMessage{json.RawMessage(`{"kind":"notice"}`)},
			},
		}
		return &App{remoteTabs: map[string]*remoteTab{tab.id: tab}}, tab
	}
	assertClosed := func(t *testing.T, tab *remoteTab) {
		t.Helper()
		if tab.routing.rehydratingPath != "" || tab.routing.rehydratingFrames != nil {
			t.Fatalf("retired generation left the provisional epoch open: path=%q frames=%d", tab.routing.rehydratingPath, len(tab.routing.rehydratingFrames))
		}
		if tab.routing.currentPath != "session-id:target" {
			t.Fatalf("retirement changed the committed route to %q", tab.routing.currentPath)
		}
	}
	t.Run("reconnect", func(t *testing.T) {
		a, tab := newTab()
		if !a.reconnectRemoteTabGeneration(tab.id, 4) {
			t.Fatal("current generation did not enter reconnecting")
		}
		assertClosed(t, tab)
	})
	t.Run("retire", func(t *testing.T) {
		a, tab := newTab()
		a.retireRemoteTabGeneration(tab.id, 4)
		assertClosed(t, tab)
	})
	t.Run("suspend", func(t *testing.T) {
		a, tab := newTab()
		a.suspendRemoteTabPumps("box", "reconnecting", "")
		assertClosed(t, tab)
	})
	t.Run("park", func(t *testing.T) {
		a, tab := newTab()
		a.parkRemoteTabsForServer("box", "~/app", "serve_down", "stopped")
		assertClosed(t, tab)
	})
	t.Run("stale generation keeps the epoch", func(t *testing.T) {
		a, tab := newTab()
		a.retireRemoteTabGeneration(tab.id, 3)
		if tab.routing.rehydratingPath != "session-id:target" {
			t.Fatal("a stale generation retired the live epoch")
		}
	})
}

// A /resume that Serve commits but whose response is lost to the tunnel, with
// Serve unreachable for the reconcile probe, hands the tab to the reattach
// loop. The reattach finds Serve already on the selected session, so no
// transition re-installs the route; the recovered tab must still accept
// commands because the provisional gate belonged to the dead generation.
func TestRemoteResumeTransportFailureReattachAcceptsCommands(t *testing.T) {
	const oldPath = "/sessions/old.jsonl"
	const targetPath = "/sessions/target.jsonl"
	fs, a := reattachSelectionFixture(t, []serveSessionEntry{
		{Name: "old", Path: oldPath, Current: true},
		{Name: "target", Path: targetPath},
	})
	events := &eventLog{}
	a.remoteEventHook = events.add
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{SessionName: "old", SessionPath: oldPath})
	statePrefix := "remote-tab:" + meta.ID + ":state "
	readyPrefix := statePrefix + `{"state":"ready"`
	waitForRemoteEventCount(t, events, readyPrefix, 1)
	a.remoteTabMu.Lock()
	initialGeneration := a.remoteTabs[meta.ID].gen
	a.remoteTabMu.Unlock()
	fs.mu.Lock()
	fs.resumeDropCount = 1
	fs.sessionsFailCount = 1
	fs.mu.Unlock()

	if _, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{SessionName: "target", SessionPath: targetPath}); err != nil {
		t.Fatal(err)
	}
	// Reattach has no artificial delay in this fixture. The whole reconnect can
	// finish between polls, so observe its retained publication sequence, not a
	// transient state value. Wait for recovery first to exercise a late observer.
	waitForRemoteEventCount(t, events, readyPrefix, 2)
	if events.count(statePrefix+`{"state":"reconnecting"`) != 1 {
		t.Fatalf("expected one published reconnect before recovery: %v", events.recorded())
	}
	sawReconnect, sawRecovered := false, false
	for _, event := range events.recorded() {
		if strings.HasPrefix(event, statePrefix+`{"state":"reconnecting"`) {
			sawReconnect = true
		} else if sawReconnect && strings.HasPrefix(event, readyPrefix) {
			sawRecovered = true
		}
	}
	if !sawRecovered {
		t.Fatalf("ready was not published after reconnect: %v", events.recorded())
	}
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[meta.ID]
	gate, route, state, generation := tab.routing.rehydratingPath, tab.routing.currentPath, tab.state, tab.gen
	a.remoteTabMu.Unlock()
	if state != "ready" || generation <= initialGeneration {
		t.Fatalf("recovery state/generation = %q/%d, want ready after generation %d", state, generation, initialGeneration)
	}
	if gate != "" {
		t.Fatalf("recovered tab still gated on %q", gate)
	}
	if route != targetPath {
		t.Fatalf("recovered route = %q, want the selected %q", route, targetPath)
	}
	if err := a.SubmitRemoteTab(meta.ID, "after recovery"); err != nil {
		t.Fatalf("submit after recovered resume: %v", err)
	}
	if _, err := a.RemoteTabStatus(meta.ID); err != nil {
		t.Fatalf("status after recovered resume: %v", err)
	}
}

// Re-selecting the session a ready tab already shows is not a switch: the
// registration must not open the provisional gate, so commands keep flowing
// while the idempotent /resume is in flight.
func TestReselectingCurrentRemoteSessionDoesNotEnterSwitchingState(t *testing.T) {
	const path = "/sessions/s1.jsonl"
	fs, a := reattachSelectionFixture(t, []serveSessionEntry{{Name: "s1", Path: path, Current: true}})
	meta := openReadyRemoteTab(t, a, RemoteTabOpenOptions{SessionName: "s1", SessionPath: path})

	started := make(chan string, 1)
	release := make(chan struct{})
	fs.mu.Lock()
	fs.resumeStarted, fs.resumeRelease = started, release
	fs.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	if _, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{SessionName: "s1", SessionPath: path}); err != nil {
		t.Fatal(err)
	}
	a.remoteTabMu.Lock()
	gate := a.remoteTabs[meta.ID].routing.rehydratingPath
	a.remoteTabMu.Unlock()
	if gate != "" {
		t.Fatalf("re-click of the current session opened the provisional gate on %q", gate)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("re-click did not reach /resume")
	}
	if err := a.SubmitRemoteTab(meta.ID, "still here"); err != nil {
		t.Fatalf("submit while the same-route resume is in flight: %v", err)
	}
	close(release)
	waitForRemoteSessionIdentity(t, a, meta.ID, "s1", path)
	if fenced := fs.recordedExpectedPaths(); len(fenced) == 0 || fenced[len(fenced)-1] != path {
		t.Fatalf("submit fenced against %v, want %q", fenced, path)
	}
	for _, call := range fs.recorded() {
		if strings.HasPrefix(call, "POST /submit") && !strings.Contains(call, "still here") {
			t.Fatalf("unexpected submit recorded: %q", call)
		}
	}
}
