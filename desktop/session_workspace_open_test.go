package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/transcript"
)

func canonicalWorkspaceOpenFixture(t *testing.T) (*App, *WorkspaceTab, *session.Runtime, string, string) {
	t.Helper()
	isolateDesktopUserDirs(t)
	model, _ := configureSwitchableDefaultModels(t)
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.ctx = context.Background()
	// Identical display names must not collapse distinct workspace identities.
	rootA, rootB := filepath.Join(t.TempDir(), "project"), filepath.Join(t.TempDir(), "project")
	for _, root := range []string{rootA, rootB} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	workspaceA, err := app.ensureDesktopWorkspace(t.Context(), "project", rootA)
	if err != nil {
		t.Fatal(err)
	}
	workspaceB, err := app.ensureDesktopWorkspace(t.Context(), "project", rootB)
	if err != nil {
		t.Fatal(err)
	}
	service := app.desktopSessionService("")
	create := func(id, cwd, workspace string) *session.Runtime {
		runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: id, CWD: cwd, Origin: session.SessionOriginNew})
		if err != nil {
			t.Fatal(err)
		}
		appendSessionTestModel(t, runtime, id+"-model", model)
		appendSessionTestMessage(t, runtime, id+"-system", provider.Message{ID: id + "-system", Role: provider.RoleSystem, Content: "test system"})
		appendSessionTestMessage(t, runtime, id+"-message", provider.Message{ID: id + "-user", Role: provider.RoleUser, Content: id})
		if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspace, id, ""); err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	source, target := create("session-A", rootA, workspaceA), create("session-B", rootB, workspaceB)
	ctrl, err := app.buildTabControllerBoot(app.ctx, boot.Options{Model: model, WorkspaceRoot: rootA, SessionDir: desktopSessionDir(rootA), Sink: event.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.(control.IdentityLifecycle).OpenSession(t.Context(), source.Ref()); err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{ID: "source-tab", Scope: "project", WorkspaceRoot: rootA, SessionID: source.Ref().SessionID, Ready: true, Ctrl: ctrl, model: model, sink: &tabEventSink{tabID: "source-tab", app: app}, disabledMCP: map[string]ServerView{}}
	tab.SessionWorkspace.ID = workspaceA
	app.tabs[tab.ID], app.tabOrder, app.activeTabID = tab, []string{tab.ID}, tab.ID
	app.newSessionRuntimeLocked(tab, sessionRuntimeKey(tab.currentSessionIdentity()))
	t.Cleanup(func() {
		if live := app.controllerForTab(tab); live != nil {
			live.Close()
		}
		if tab.SharedHostKey != "" {
			app.releaseSharedHost(tab.SharedHostKey)
		}
	})
	return app, tab, target, rootB, workspaceB
}

func TestCanonicalOpenRefreshesSameWorkspaceIdentity(t *testing.T) {
	app, tab, _, _, _ := canonicalWorkspaceOpenFixture(t)
	want, ctrl := tab.SessionWorkspace.ID, tab.Ctrl
	tab.SessionWorkspace.ID = "stale-workspace"
	if _, err := app.OpenSession(session.SessionRef{HostID: localDesktopHostID, SessionID: tab.SessionID}); err != nil {
		t.Fatal(err)
	}
	if tab.SessionWorkspace.ID != want || tab.Ctrl != ctrl {
		t.Fatal("same-session navigation did not reconcile its workspace identity")
	}
}

func TestCanonicalOpenFailurePreservesSource(t *testing.T) {
	app, tab, target, _, _ := canonicalWorkspaceOpenFixture(t)
	previous := persistedDesktopTabEntry(tab)
	ctrl := tab.Ctrl
	if err := app.desktopSessionService("").Close(t.Context(), target.Ref()); err != nil {
		t.Fatal(err)
	}
	other, err := session.NewService(localDesktopHostID, session.NewFilesystemPersistence(app.desktopSessions.root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Shutdown(context.Background()) })
	lease, err := other.Open(t.Context(), target.Ref())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release(context.Background()) })
	if _, err := app.OpenSession(target.Ref()); err == nil {
		t.Fatal("external writer conflict accepted")
	}
	if tab.Ctrl != ctrl || tab.SessionID != previous.SessionID || tab.WorkspaceRoot != previous.WorkspaceRoot || !tab.Ready {
		t.Fatal("failed open replaced source")
	}
	if err := ctrl.Snapshot(); err != nil {
		t.Fatalf("source lost its writer: %v", err)
	}
}

func TestEmbeddedBotSessionOpensReadOnlyFromSidebar(t *testing.T) {
	app, _, _, root, _ := canonicalWorkspaceOpenFixture(t)
	botRoot := session.RootForLegacyDir(config.ProjectSessionDir(root))
	bot, err := app.historicalSessionService(botRoot)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := bot.Create(t.Context(), session.CreateOptions{SessionID: "bot-conversation", CWD: root, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	appendSessionTestMessage(t, runtime, "bot-message", provider.Message{ID: "bot-message", Role: provider.RoleAssistant, Content: "reply from bot"})
	meta, err := app.OpenTopicSession("project", root, "", embeddedBotSessionPrefix+runtime.Ref().SessionID)
	if err != nil {
		t.Fatalf("open project bot session from Bots sidebar: %v", err)
	}
	if !meta.ReadOnly {
		t.Fatal("bot transcript was opened writable")
	}
	opened := waitForTabReady(t, app, meta.ID)
	if opened.Ctrl == nil || !opened.ReadOnly {
		t.Fatal("bot transcript did not finish as a read-only controller")
	}
	if history := opened.Ctrl.History(); len(history) == 0 || history[len(history)-1].Content != "reply from bot" {
		t.Fatalf("read project bot history: %+v", history)
	}
	update, err := json.Marshal(map[string]any{"message": provider.Message{ID: "bot-live", Role: provider.RoleAssistant, Content: "continued from bot"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().AppendBatch(t.Context(), "bot-live", []session.Event{{Kind: "message/complete", Payload: update}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if history := opened.Ctrl.History(); len(history) == 0 || history[len(history)-1].Content != "continued from bot" {
		t.Fatalf("desktop missed bot's live append: %+v", history)
	}
	snapshot, err := app.TranscriptSnapshotForTab(meta.ID, transcript.PageRequest{})
	if err != nil || snapshot.Identity.SessionID != runtime.Ref().SessionID || snapshot.TotalRecords < 2 {
		t.Fatalf("desktop transcript projection missed bot update: %+v, %v", snapshot, err)
	}
}

func TestEmbeddedBotSessionOpenRejectsAnotherWorkspace(t *testing.T) {
	app, tab, _, root, _ := canonicalWorkspaceOpenFixture(t)
	botRoot := session.RootForLegacyDir(config.ProjectSessionDir(root))
	bot, err := app.historicalSessionService(botRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bot.Create(t.Context(), session.CreateOptions{SessionID: "wrong-workspace", CWD: tab.WorkspaceRoot, Origin: session.SessionOriginNew}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.OpenTopicSession("project", root, "", embeddedBotSessionPrefix+"wrong-workspace"); !errors.Is(err, errSessionWorkspaceConflict) {
		t.Fatalf("cross-workspace bot session was accepted: %v", err)
	}
}

func TestEmbeddedGlobalBotSessionOpensFromSidebar(t *testing.T) {
	app, _, _, _, _ := canonicalWorkspaceOpenFixture(t)
	root := globalTabWorkspaceRoot()
	botRoot := session.RootForLegacyDir(config.ProjectSessionDir(root))
	bot, err := app.historicalSessionService(botRoot)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := bot.Create(t.Context(), session.CreateOptions{SessionID: "global-bot", CWD: root, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	appendSessionTestMessage(t, runtime, "global-bot-message", provider.Message{ID: "global-bot-message", Role: provider.RoleAssistant, Content: "global bot reply"})
	meta, err := app.OpenTopicSession("global", "", "", embeddedBotSessionPrefix+runtime.Ref().SessionID)
	if err != nil {
		t.Fatal(err)
	}
	opened := waitForTabReady(t, app, meta.ID)
	if !opened.ReadOnly {
		t.Fatal("global bot transcript opened writable")
	}
	if history := opened.Ctrl.History(); len(history) == 0 || history[len(history)-1].Content != "global bot reply" {
		t.Fatalf("global bot history = %+v", history)
	}
}

func TestCanonicalOpenRejectsConflictingMembership(t *testing.T) {
	app, tab, target, _, workspaceB := canonicalWorkspaceOpenFixture(t)
	ctrl := tab.Ctrl
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	owner := state.Workspaces[workspaceB]
	owner.Root = tab.WorkspaceRoot
	state.Workspaces[workspaceB] = owner
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app.workspaceRegistry().Path(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.OpenSession(target.Ref()); !errors.Is(err, errSessionWorkspaceConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if tab.Ctrl != ctrl || tab.SessionID != "session-A" {
		t.Fatal("conflict modified current runtime")
	}
	if got := friendlySessionLoadError(errSessionWorkspaceConflict); !errors.Is(got, errSessionWorkspaceConflict) {
		t.Fatalf("conflict disguised as corruption: %v", got)
	}
}

func TestCanonicalStartupRepairsPersistedWorkspace(t *testing.T) {
	app, tab, target, rootB, workspaceB := canonicalWorkspaceOpenFixture(t)
	tab.Ctrl.Close()
	tab.Ctrl, tab.Ready = nil, false
	tab.SessionID = target.Ref().SessionID
	tab.TopicID, tab.TopicTitle = "old-topic", "old title"
	tab.SessionPath = "/old/project/transcript.jsonl"
	app.saveTabsFromRemote()
	if !app.prepareTabControllerWorkspace(tab, t.Context(), 0, app.ctx) {
		t.Fatal(tab.StartupErr)
	}
	if tab.WorkspaceRoot != rootB || tab.SessionWorkspace.ID != workspaceB || tab.TopicID != "" || tab.SessionPath != "" {
		t.Fatalf("cold repair retained old binding: %+v", persistedDesktopTabEntry(tab))
	}
	app.buildTabController(tab)
	if tab.Ctrl == nil || !tab.Ready || tab.StartupErr != "" {
		t.Fatalf("restart failed: %s", tab.StartupErr)
	}
	if root, ok := safeControllerWorkspaceRoot(tab.Ctrl); !ok || !sameDesktopPath(root, rootB) {
		t.Fatalf("restart root = %s", root)
	}
}

func TestCanonicalOpenRecoversFailedSurface(t *testing.T) {
	app, tab, target, rootB, _ := canonicalWorkspaceOpenFixture(t)
	tab.Ctrl.Close()
	tab.Ctrl, tab.Ready, tab.StartupErr = nil, false, errSessionWorkspaceConflict.Error()
	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatal(err)
	}
	if !tab.Ready || tab.StartupErr != "" || !sameDesktopPath(tab.WorkspaceRoot, rootB) {
		t.Fatal("failed surface did not recover")
	}
}

func TestCanonicalOpenCommitsTargetWorkspace(t *testing.T) {
	app, tab, target, rootB, workspaceB := canonicalWorkspaceOpenFixture(t)
	tab.HistoricalSource = &SessionSourceRef{Path: "/fixture/old.jsonl"}
	if err := app.workspaceRegistry().EnsureSessionTopic(t.Context(), target.Ref().SessionID, "topic-B", "Target topic"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatalf("open: %v", err)
	}
	if tab.SessionID != target.Ref().SessionID || !sameDesktopPath(tab.WorkspaceRoot, rootB) || tab.SessionWorkspace.ID != workspaceB {
		t.Fatalf("wrong destination binding; got session=%s root=%s workspace=%s", tab.SessionID, tab.WorkspaceRoot, tab.SessionWorkspace.ID)
	}
	if tab.TopicID != "topic-B" || tab.TopicTitle != "Target topic" {
		t.Fatalf("wrong destination topic; got id=%q title=%q", tab.TopicID, tab.TopicTitle)
	}
	if tab.HistoricalSource != nil {
		t.Fatal("canonical activation retained the preparation action")
	}
	persisted := loadTabsFile()
	if len(persisted.Tabs) != 1 || persisted.Tabs[0].SessionID != "session-B" || persisted.Tabs[0].TopicID != "topic-B" || !sameDesktopPath(persisted.Tabs[0].WorkspaceRoot, rootB) {
		t.Fatalf("destination binding not persisted: %+v", persisted)
	}
	err := app.validateDesktopWorkspaceMembership(t.Context(), workspaceB, target.Ref())
	if err != nil {
		t.Fatalf("restart validation: %v", err)
	}
	if root, ok := safeControllerWorkspaceRoot(tab.Ctrl); !ok || !sameDesktopPath(root, rootB) {
		t.Fatalf("controller workspace = %q, available=%v", root, ok)
	}
}
