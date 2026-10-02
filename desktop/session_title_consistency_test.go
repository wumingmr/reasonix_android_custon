package main

import (
	"os"
	"testing"
	"time"

	"reasonix/internal/session"
)

// sidebarLabelForSession returns the label the project tree paints for ref.
func sidebarLabelForSession(t *testing.T, app *App, root string, ref session.SessionRef) string {
	t.Helper()
	page, err := app.unifiedProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 50})
	if err != nil {
		t.Fatalf("sidebar page: %v", err)
	}
	for _, node := range page.Items {
		if node.Session != nil && node.Session.SessionID == ref.SessionID {
			return node.Label
		}
	}
	t.Fatalf("session %q missing from the sidebar page", ref.SessionID)
	return ""
}

func canonicalTitleFixture(t *testing.T) (*App, *WorkspaceTab, *session.Runtime, session.SessionRef) {
	t.Helper()
	app, tab, target, _, _ := canonicalWorkspaceOpenFixture(t)
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: tab.SessionID}
	// Desktop session creation seeds the presentation row with the auto
	// default while the session log itself carries no title yet.
	if err := app.workspaceRegistry().EnsureSessionTopic(t.Context(), ref.SessionID, "topic-A", defaultTopicTitle); err != nil {
		t.Fatal(err)
	}
	if _, err := app.OpenSession(ref); err != nil {
		t.Fatal(err)
	}
	return app, tab, target, ref
}

// An untitled session used to show its first user turn in the sidebar and the
// presentation default in the topicbar. Both surfaces share one projection.
func TestUntitledCanonicalSessionSurfacesAgree(t *testing.T) {
	app, tab, _, ref := canonicalTitleFixture(t)
	topicbar := app.tabMeta(tab, true).TopicTitle
	sidebar := sidebarLabelForSession(t, app, tab.WorkspaceRoot, ref)
	if topicbar != sidebar {
		t.Fatalf("topicbar %q != sidebar %q", topicbar, sidebar)
	}
	if topicbar != "session-A" {
		t.Fatalf("untitled session must show its first user turn, got %q", topicbar)
	}
}

// A canonical rename writes only the session log. Every tab bind, including
// the restart rebuild that starts from the legacy topic map, must re-derive
// the name from that log, and the presentation row must mirror it.
func TestCanonicalRenameSurvivesSwitchAndRebuild(t *testing.T) {
	app, tab, target, ref := canonicalTitleFixture(t)
	rootA := tab.WorkspaceRoot
	const want = "AI 生成的标题"
	if err := app.RenameCanonicalSession(ref, want); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if tab.TopicTitle != want || tab.topicTitleSource != topicTitleSourceManual {
		t.Fatalf("live tab after rename = %q/%q", tab.TopicTitle, tab.topicTitleSource)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Presentation[ref.SessionID].Title; got != want {
		t.Fatalf("presentation title = %q, want write-through %q", got, want)
	}

	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatalf("switch away: %v", err)
	}
	if _, err := app.OpenSession(ref); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	if got := app.tabMeta(tab, true).TopicTitle; got != want {
		t.Fatalf("topicbar after switching back = %q, want %q", got, want)
	}
	if got := sidebarLabelForSession(t, app, rootA, ref); got != want {
		t.Fatalf("sidebar after rename = %q, want %q", got, want)
	}

	// Restart: restoreOrBuildTabs recreates every local tab through
	// createTabEntryWithID and then builds its controller. Park the visible
	// surface on B first so A is cold, exactly like a fresh process.
	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatalf("park on B: %v", err)
	}
	const restoredID = "restored-tab"
	restored := app.createTabEntryWithID("project", rootA, "topic-A", restoredID)
	if restored.TopicTitle == want {
		t.Fatal("fixture must start from the legacy topic map, not the log")
	}
	restored.SessionID = ref.SessionID
	restored.sink = &tabEventSink{tabID: restoredID, app: app}
	app.mu.Lock()
	app.tabs[restoredID] = restored
	app.tabOrder = append(app.tabOrder, restoredID)
	app.activeTabID = restoredID
	app.mu.Unlock()
	app.startTabControllerBuild(restored)
	built := waitForTabReady(t, app, restoredID)
	if got := app.tabMeta(built, true).TopicTitle; got != want {
		t.Fatalf("topicbar after restart rebuild = %q, want %q", got, want)
	}
}

// Clearing a committed title must not leave the topicbar blank while the
// sidebar shows the preview: the log owns the name, the shared chain closes it.
func TestClearedCanonicalTitleFallsBackToPreviewEverywhere(t *testing.T) {
	app, tab, _, ref := canonicalTitleFixture(t)
	if err := app.RenameCanonicalSession(ref, "临时标题"); err != nil {
		t.Fatal(err)
	}
	if err := app.RenameCanonicalSession(ref, ""); err != nil {
		t.Fatal(err)
	}
	topicbar := app.tabMeta(tab, true).TopicTitle
	sidebar := sidebarLabelForSession(t, app, tab.WorkspaceRoot, ref)
	if topicbar != "session-A" || sidebar != "session-A" {
		t.Fatalf("cleared title: topicbar %q sidebar %q, want the first user turn", topicbar, sidebar)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Presentation[ref.SessionID].Title; got != "" {
		t.Fatalf("presentation title after clear = %q, want empty", got)
	}
}

// Legacy sessions had the opposite asymmetry: the rename reached the sidebar
// row but never the open tab bound to that file.
func TestLegacySessionRenameProjectsIntoOpenTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	dir := desktopSessionDir(globalWorkspaceRoot())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const topicID = "topic_legacy_title"
	if err := setTopicTitle("", topicID, "旧标题"); err != nil {
		t.Fatal(err)
	}
	path := writeTopicSessionWithPrompt(t, dir, "legacy-title.jsonl", topicID, "旧标题", "", "legacy prompt", time.Now())
	tab := &WorkspaceTab{
		ID: "legacy-tab", Scope: "global", TopicID: topicID, TopicTitle: "旧标题",
		topicTitleSource: topicTitleSourceManual, SessionPath: path, disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	if err := app.RenameSession(path, "AI 标题"); err != nil {
		t.Fatalf("rename legacy session: %v", err)
	}
	if tab.TopicTitle != "AI 标题" || tab.topicTitleSource != topicTitleSourceManual {
		t.Fatalf("open legacy tab after rename = %q/%q", tab.TopicTitle, tab.topicTitleSource)
	}
	if err := app.RenameSession(path, ""); err != nil {
		t.Fatalf("clear legacy session title: %v", err)
	}
	if tab.TopicTitle != "旧标题" {
		t.Fatalf("cleared legacy title must fall back to the topic title, got %q", tab.TopicTitle)
	}
}
