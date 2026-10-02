package main

import (
	"testing"

	"reasonix/internal/session"
)

func TestSingleSurfaceReopenUsesNewTabBindingForStableSession(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	const sessionID = "canonical-session-a"
	if _, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{
		SessionID: sessionID, CWD: globalWorkspaceRoot(), Origin: session.SessionOriginNew,
	}); err != nil {
		t.Fatal(err)
	}
	app.tabs = map[string]*WorkspaceTab{
		"a": {ID: "a", Scope: "global", TopicID: "topic_a", TopicTitle: "a", SessionID: sessionID, disabledMCP: map[string]ServerView{}},
		"b": {ID: "b", Scope: "global", TopicID: "topic_b", TopicTitle: "b", disabledMCP: map[string]ServerView{}},
	}
	app.tabOrder = []string{"a", "b"}
	app.activeTabID = "a"
	oldTabID := app.tabs["a"].ID

	if _, err := app.keepOnlyVisibleTab("b"); err != nil {
		t.Fatalf("keepOnlyVisibleTab: %v", err)
	}
	if app.tabs[oldTabID] != nil {
		t.Fatalf("old tab %q survived single-surface pruning", oldTabID)
	}

	app.mu.Lock()
	newTabID := app.newUniqueTabIDLocked()
	app.tabs[newTabID] = &WorkspaceTab{
		ID: newTabID, Scope: "global", TopicID: "topic_a", TopicTitle: "a",
		SessionID: sessionID, disabledMCP: map[string]ServerView{},
	}
	app.mu.Unlock()
	if newTabID == oldTabID {
		t.Fatalf("reopened tab reused pruned id %q", oldTabID)
	}
	if got := app.tabs[newTabID].SessionID; got != sessionID {
		t.Fatalf("reopened session id = %q, want stable %q", got, sessionID)
	}
}
