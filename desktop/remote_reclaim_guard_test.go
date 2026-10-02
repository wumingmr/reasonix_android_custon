package main

import (
	"net/http"
	"strings"
	"testing"
)

// ReclaimRemoteTabSession releases remoteTabMu and long-polls Serve for up to
// 20s, then re-locks the tab it captured earlier. The tab can close in the
// window between the command-target read and that capture, so the capture must
// prove the binding exists: dereferencing a missing entry panics inside a
// Wails binding and takes the window down instead of failing the button.
func TestReclaimObservationRejectsMissingOrReplacedBinding(t *testing.T) {
	client := &http.Client{}
	a := &App{remoteTabs: map[string]*remoteTab{}}
	tab := &remoteTab{id: "remote-1", state: "ready", gen: 4, client: client,
		runtime: remoteTabRuntimeState{revision: 7}, selectionRevision: 2}
	a.remoteTabs[tab.id] = tab

	observed, err := a.observeRemoteTabForReclaim(tab.id, client)
	if err != nil {
		t.Fatalf("live binding was rejected: %v", err)
	}
	if observed.tab != tab || observed.gen != 4 || observed.runtimeRevision != 7 || observed.selectionRevision != 2 {
		t.Fatalf("observation = %+v, want the live tab's fences", observed)
	}

	// Closed between the command-target read and the snapshot.
	delete(a.remoteTabs, tab.id)
	closedObservation, err := a.observeRemoteTabForReclaim(tab.id, client)
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("closed tab observation = %+v, %v; want a not-connected error", closedObservation, err)
	}
	if closedObservation.tab != nil {
		t.Fatal("closed tab observation carried a binding forward")
	}

	// Reconnected in the same window: a new client means the captured target
	// belongs to a retired generation.
	a.remoteTabs[tab.id] = tab
	tab.client = &http.Client{}
	if _, err := a.observeRemoteTabForReclaim(tab.id, client); err == nil {
		t.Fatal("observation accepted a replaced client binding")
	}
}

// The public entry point reports the same disconnected error rather than
// panicking when the tab is gone.
func TestReclaimRemoteTabSessionRejectsClosedTab(t *testing.T) {
	a := &App{remoteTabs: map[string]*remoteTab{}}
	if err := a.ReclaimRemoteTabSession("missing"); err == nil {
		t.Fatal("reclaim on a missing tab reported success")
	}
}
