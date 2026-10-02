package main

import (
	"testing"
)

// The blank-row filter hides never-chatted canonical sessions the way an
// unused local blank disappears. A pin is an explicit user decision to keep a
// row, so it must be consulted before the row is dropped — otherwise pinning a
// fresh session makes it vanish from the sidebar.
func TestPinnedZeroTurnSessionSurvivesTheBlankRowFilter(t *testing.T) {
	seedBridgeTestHost(t, "box")
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{
		{Name: "kept", SessionID: "kept-id", Turns: 0, MetadataReady: true},
		{Name: "dropped", SessionID: "dropped-id", Turns: 0, MetadataReady: true},
		{Name: "chatted", SessionID: "chatted-id", Turns: 3, MetadataReady: true},
	})
	kernel := &fakeRemoteKernel{
		statuses:    []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView:  RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL},
		ensureToken: "s3cret",
	}
	a := &App{remoteRuntime: kernel}

	before, err := a.RemoteProjectSessions("box", "~/app")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].Name != "chatted" {
		t.Fatalf("unpinned listing = %+v, want only the chatted session", before)
	}

	if err := a.SetRemoteSessionPinned("box", "~/app", "kept", true); err != nil {
		t.Fatal(err)
	}
	after, err := a.RemoteProjectSessions("box", "~/app")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 || after[0].Name != "kept" || !after[0].Pinned {
		t.Fatalf("pinned listing = %+v, want the pinned never-chatted row first", after)
	}
	for _, row := range after {
		if row.Name == "dropped" {
			t.Fatalf("unpinned never-chatted row resurfaced: %+v", after)
		}
	}
}
