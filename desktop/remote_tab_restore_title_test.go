package main

import (
	"testing"
)

// The opaque-ID title cleanup exists because older builds persisted a
// canonical session ID as the tab title. A legacy row has no canonical
// identity and its name is the transcript basename, which the sidebar also
// shows as the label — discarding it there makes the tab read "app" while the
// sidebar reads "chat-2026-09" after every restart.
func TestRestoreKeepsLegacyBasenameTitleAndDropsOpaqueIdentity(t *testing.T) {
	seedBridgeTestHost(t, "box")
	a := &App{}
	a.restoreRemoteTabShells(desktopTabsFile{
		RemoteTabs: []desktopRemoteTabEntry{
			{ID: "legacy", HostID: "box", Workspace: "~/app", TopicTitle: "chat-2026-09", SessionName: "chat-2026-09", SessionPath: "/sessions/chat-2026-09.jsonl"},
			{ID: "canonical", HostID: "box", Workspace: "~/app2", TopicTitle: "abc123", SessionName: "abc123", SessionID: "abc123"},
		},
		RemoteTabOrder: []string{"legacy", "canonical"},
	})

	a.remoteTabMu.Lock()
	defer a.remoteTabMu.Unlock()
	if got := a.remoteTabs["legacy"].topicTitle; got != "chat-2026-09" {
		t.Fatalf("legacy restored title = %q, want the persisted basename", got)
	}
	if got := a.remoteTabs["canonical"].topicTitle; got != remoteWorkspaceName("~/app2") {
		t.Fatalf("canonical restored title = %q, want the workspace fallback", got)
	}
}
