package main

import (
	"testing"
	"time"
)

// TestRoutedAttachAnnouncesIdentityWhileConnecting pins the history-first half
// of the attach publication: an attach that commits a session route announces
// that tab while it is still connecting, so the frontend can read the session's
// persisted window before /resume activates the remote runtime. The other half
// — a routeless (fresh-session) attach stays silent so the bootstrap's own
// ready meta is the sidebar's first update — lives in
// TestBootstrapNewSessionPublishesTabUpdateForSidebarBlankRow.
func TestRoutedAttachAnnouncesIdentityWhileConnecting(t *testing.T) {
	const sessionPath = "/remote/sessions/s1.jsonl"
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{
		{Name: "s1", Path: sessionPath, Title: "First chat", Turns: 2, Current: true},
	})
	kernel := &fakeRemoteKernel{
		statuses:    []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView:  RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL},
		ensureToken: "s3cret",
	}
	seedBridgeTestHost(t, "box")
	log := &eventLog{}
	updates := make(chan TabMeta, 4)
	a := &App{remoteRuntime: kernel, remoteEventHook: func(name string, payload any) {
		log.add(name, payload)
		if name != "remote-tab:updated" {
			return
		}
		if meta, ok := payload.(TabMeta); ok {
			select {
			case updates <- meta:
			default:
			}
		}
	}}
	cleanupRemoteTabPumps(t, a)
	meta, err := a.OpenRemoteProjectTab("box", "~/app", RemoteTabOpenOptions{SessionPath: sessionPath})
	if err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	var update TabMeta
	for update.ID != meta.ID {
		select {
		case update = <-updates:
		case <-timer.C:
			t.Fatalf("routed attach emitted no remote-tab:updated for tab %q, events: %v", meta.ID, log.recorded())
		}
	}
	if update.SessionPath != sessionPath {
		t.Fatalf("attach announcement session = %q, want the routed %q", update.SessionPath, sessionPath)
	}
	if update.RemoteState != "connecting" {
		t.Fatalf("attach announcement state = %q, want it before the ready rotation", update.RemoteState)
	}
	waitForTabState(t, a, meta.ID, "ready")
}
