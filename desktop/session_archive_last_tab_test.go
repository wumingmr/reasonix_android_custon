package main

import (
	"os"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
)

// Archiving the only visible session hands the surface to the empty welcome.
// A replacement blank session would be registered as a real sidebar row that
// the user can archive again, so the workspace could never become empty.
func TestArchiveLastVisibleSessionLeavesNoReplacementBlankSession(t *testing.T) {
	a, ref := lifecycleFixture(t)
	a.tabs = map[string]*WorkspaceTab{
		"only": {ID: "only", Scope: "global", WorkspaceRoot: globalWorkspaceRoot(), TopicID: "canonical-" + ref.SessionID,
			SessionID: ref.SessionID, Ready: true, disabledMCP: map[string]ServerView{}},
	}
	a.tabOrder = []string{"only"}
	a.activeTabID = "only"

	archived, err := a.ArchiveSessionTarget(SessionSelector{Ref: &ref})
	if err != nil || !archived.Committed {
		t.Fatalf("ArchiveSessionTarget = %+v, %v", archived, err)
	}
	assertNoVisibleRuntime(t, a)
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.SessionStates[ref.SessionID].Lifecycle; got != workspacestate.Archived {
		t.Fatalf("archived lifecycle = %q", got)
	}
	for id, sessionState := range state.SessionStates {
		if sessionState.Lifecycle == workspacestate.Active {
			t.Fatalf("archive registered replacement session %q", id)
		}
	}
	page, err := a.ListWorkspaceSessions(workspacestate.GlobalWorkspaceID, "", "", 50, false)
	if err != nil || len(page.Sessions) != 0 {
		t.Fatalf("workspace still lists sessions after archiving its only one: %+v, %v", page.Sessions, err)
	}
}

// The legacy topic archive shares the fallback mechanism with canonical
// archive, so it must leave the same empty surface.
func TestTrashLastTopicLeavesNoReplacementBlankSession(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectRoot := t.TempDir()
	topicID := "topic_trash_last"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := setTopicTitle(projectRoot, topicID, "Trash last"); err != nil {
		t.Fatalf("set topic title: %v", err)
	}
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	sessionPath := writeTopicSession(t, dir, "trash-last.jsonl", topicID, "Trash last", projectRoot)
	ctrl := controllerWithContent(t, sessionPath)
	tab := &WorkspaceTab{ID: "only", Scope: "project", WorkspaceRoot: projectRoot, TopicID: topicID,
		TopicTitle: "Trash last", SessionPath: sessionPath, Ctrl: ctrl, Ready: true, disabledMCP: map[string]ServerView{}}
	app := NewApp()
	app.ctx = t.Context()
	installNoopRuntimeEvents(app)
	t.Cleanup(app.closeSessionServices)
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	if err := app.TrashTopic(topicID); err != nil {
		t.Fatalf("TrashTopic: %v", err)
	}
	assertNoVisibleRuntime(t, app)
}

func assertNoVisibleRuntime(t *testing.T, a *App) {
	t.Helper()
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.tabs) != 0 || len(a.tabOrder) != 0 || a.activeTabID != "" {
		ids := make([]string, 0, len(a.tabs))
		for id, tab := range a.tabs {
			ids = append(ids, id+"/topic="+tab.TopicID+"/session="+tab.SessionID)
		}
		t.Fatalf("removing the last visible session opened a replacement runtime: tabs=%v order=%v active=%q", ids, a.tabOrder, a.activeTabID)
	}
}
