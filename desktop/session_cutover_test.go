package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestTabMetaDoesNotCompareLegacyFingerprintWithCanonicalSession(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	sessionPath := filepath.Join(t.TempDir(), "legacy.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(sessionPath, agent.BranchMeta{
		ID: "legacy", Revision: 41, ContentDigest: "legacy-digest",
	}); err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{
		ID: "canonical-without-head", SessionID: "missing-canonical-session",
		SessionPath: sessionPath, disabledMCP: map[string]ServerView{},
	}
	meta := app.tabMeta(tab, true)
	if meta.SessionRevision != 0 || meta.SessionDigest != "" {
		t.Fatalf("canonical tab fell back to legacy fingerprint (%d, %q)", meta.SessionRevision, meta.SessionDigest)
	}
}

func TestDesktopHistorySliceUsesCanonicalDurableIndex(t *testing.T) {
	isolateDesktopUserDirs(t)
	model, _ := configureSwitchableDefaultModels(t)
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.ctx = context.Background()
	root := t.TempDir()
	dir := desktopSessionDir(root)
	service := app.desktopSessionService(dir)
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "canonical-history"})
	if err != nil {
		t.Fatal(err)
	}
	appendSessionTestMessage(t, runtime, "history-user", provider.Message{ID: "history-user", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: "durable user turn"})

	ctrl, err := app.buildTabControllerBoot(app.ctx, boot.Options{Model: model, WorkspaceRoot: root, SessionDir: dir, Sink: event.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.(control.IdentityLifecycle).OpenSession(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}
	large := "durable assistant " + strings.Repeat("x", historyInlineRefThreshold+1024)
	// Append after the controller's agent projection was created. The legacy
	// live-history path cannot see this message; the canonical query can.
	appendSessionTestMessage(t, runtime, "history-assistant", provider.Message{ID: "history-assistant", Role: provider.RoleAssistant, Content: large})
	legacyPath := filepath.Join(dir, "legacy-projection.jsonl")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{ID: "canonical-history-tab", Scope: "project", WorkspaceRoot: root, SessionID: runtime.Ref().SessionID, SessionPath: legacyPath, Ready: true, Ctrl: ctrl, sink: &tabEventSink{tabID: "canonical-history-tab", app: app}, disabledMCP: map[string]ServerView{}}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() { ctrl.Close() })

	openView, err := app.SessionOpenForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(legacyPath, agent.BranchMeta{
		ID:            "legacy-projection",
		Revision:      int64(openView.SnapshotSequence) + 100,
		ContentDigest: "legacy-projection-digest",
	}); err != nil {
		t.Fatal(err)
	}
	meta := app.tabMeta(tab, true)
	if meta.SessionRevision != int64(openView.SnapshotSequence) || meta.SessionDigest != openView.StorageGeneration {
		t.Fatalf("tab canonical fingerprint = (%d, %q), want (%d, %q)",
			meta.SessionRevision, meta.SessionDigest, openView.SnapshotSequence, openView.StorageGeneration)
	}

	page := app.HistorySliceForTab(tab.ID, HistorySliceRequest{Turns: 12, Entries: 120, Bytes: 512 << 10})
	if page.Error != "" || page.Source != "canonical-index" {
		t.Fatalf("canonical history page = source %q error %q", page.Source, page.Error)
	}
	if page.TotalTurns != 1 || len(page.Entries) != 2 {
		t.Fatalf("canonical history shape = turns %d entries %d, want 1/2", page.TotalTurns, len(page.Entries))
	}
	assistant := page.Entries[1]
	if len(assistant.Refs) != 1 || assistant.Refs[0].Field != "content" {
		t.Fatalf("large canonical message refs = %+v", assistant.Refs)
	}
	chunk := app.HistoryContentForTab(tab.ID, assistant.Refs[0], 0)
	if chunk.Stale || !chunk.Done || chunk.Data != large {
		t.Fatalf("canonical expanded content = stale:%v done:%v bytes:%d, want %d", chunk.Stale, chunk.Done, len(chunk.Data), len(large))
	}

	appendSessionTestMessage(t, runtime, "history-next", provider.Message{ID: "history-next", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: "new turn"})
	if stale := app.HistoryContentForTab(tab.ID, assistant.Refs[0], 0); !stale.Stale {
		t.Fatal("content ref from older durable snapshot must become stale after append")
	}
	// Stop/recovery keeps message identities while rewriting their transcript.
	// All desktop readers must accept the runtime's reason metadata and index
	// new versions without colliding with the previously displayed messages.
	payload, err := json.Marshal(map[string]any{
		"reason": "cancel-or-recovery-rewrite",
		"messages": []provider.Message{
			{ID: "history-user", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: "durable user turn"},
			{ID: "history-assistant", Role: provider.RoleAssistant, Content: large},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Append(t.Context(), session.Batch{OperationID: "cancel-rewrite", Events: []session.Event{{Kind: "history/replace", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if page := app.HistorySliceForTab(tab.ID, HistorySliceRequest{Turns: 12}); page.Error != "" || len(page.Entries) != 2 {
		t.Fatalf("compatibility reader after cancel rewrite: %+v", page)
	}
	if page, err := app.SessionHistoryWindowForTab(tab.ID, session.HistoryWindowRequest{Anchor: "newest"}); err != nil || page.Status != "ready" || len(page.Messages) != 2 {
		t.Fatalf("window reader after cancel rewrite: %+v, %v", page, err)
	}
	if page, err := app.SessionHistoryPageForTab(tab.ID, "", 10); err != nil || page.Status != "ready" || len(page.Messages) != 2 {
		t.Fatalf("page reader after cancel rewrite: %+v, %v", page, err)
	}
}

func TestDesktopCanonicalHistoryRemainsReadableBeforeControllerReady(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	root := t.TempDir()
	dir := desktopSessionDir(root)
	service := app.desktopSessionService(dir)
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "canonical-cold-history"})
	if err != nil {
		t.Fatal(err)
	}
	appendSessionTestMessage(t, runtime, "cold-history-user", provider.Message{
		ID:      "cold-history-user",
		Role:    provider.RoleUser,
		Origin:  provider.MessageOriginUser,
		Content: "history survives an unavailable configured model",
	})
	large := "large history survives too " + strings.Repeat("x", historyInlineRefThreshold+1024)
	appendSessionTestMessage(t, runtime, "cold-history-assistant", provider.Message{
		ID: "cold-history-assistant", Role: provider.RoleAssistant, Content: large,
	})
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspaceID, runtime.Ref().SessionID, ""); err != nil {
		t.Fatal(err)
	}

	tab := &WorkspaceTab{
		ID:            "canonical-cold-history-tab",
		Scope:         "project",
		WorkspaceRoot: root,
		SessionID:     runtime.Ref().SessionID,
		Ready:         false,
		Ctrl:          nil,
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	page := app.HistorySliceForTab(tab.ID, HistorySliceRequest{Turns: 12})
	if page.Error != "" || page.Source != "canonical-index" {
		t.Fatalf("cold canonical history page = source %q error %q", page.Source, page.Error)
	}
	if len(page.Entries) != 2 || page.Entries[0].Message.Content != "history survives an unavailable configured model" {
		t.Fatalf("cold canonical history entries = %+v", page.Entries)
	}
	if len(page.Entries[1].Refs) != 1 {
		t.Fatalf("cold canonical large history refs = %+v", page.Entries[1].Refs)
	}
	content := app.HistoryContentForTab(tab.ID, page.Entries[1].Refs[0], 0)
	if content.Stale || !content.Done || content.Data != large {
		t.Fatalf("cold canonical expanded history = stale:%v done:%v bytes:%d, want %d", content.Stale, content.Done, len(content.Data), len(large))
	}

	opened, err := app.SessionOpenForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Ref != runtime.Ref() || len(opened.Recent.Entries) != 2 || opened.Recent.Entries[0].Preview != "history survives an unavailable configured model" {
		t.Fatalf("cold canonical session open = %+v", opened)
	}

	canonical, err := app.SessionHistoryPageForTab(tab.ID, "", 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical.Messages) != 2 || canonical.Messages[0].Preview != "history survives an unavailable configured model" {
		t.Fatalf("cold canonical history records = %+v", canonical.Messages)
	}
	var search session.SearchHistoryPage
	for deadline := time.Now().Add(5 * time.Second); search.Status != "ready"; time.Sleep(time.Millisecond) {
		search, err = app.SearchSessionHistoryForTab(tab.ID, "unavailable configured model", "", 12)
		if err != nil || (search.Status != "preparing" && search.Status != "ready") || time.Now().After(deadline) {
			t.Fatalf("cold canonical search = %+v, %v", search, err)
		}
	}
	if len(search.Hits) != 1 || search.Hits[0].MessageID != "cold-history-user" {
		t.Fatalf("cold canonical search hits = %+v", search.Hits)
	}
	location, err := app.LocateSessionMessageForTab(tab.ID, "cold-history-assistant", canonical.SnapshotSequence)
	if err != nil || location.Status != "ready" || location.MessageID != "cold-history-assistant" {
		t.Fatalf("cold canonical location = %+v, %v", location, err)
	}
	ref := canonical.Messages[1].ContentRef
	if ref == nil {
		t.Fatal("cold canonical large message has no content ref")
	}
	chunk, err := app.SessionHistoryContentForTab(tab.ID, *ref, 0)
	decoded, decodeErr := base64.StdEncoding.DecodeString(chunk.Data)
	if err != nil || decodeErr != nil || !chunk.Done || !strings.Contains(string(decoded), large) {
		t.Fatalf("cold canonical content = done:%v bytes:%d, errors:%v/%v", chunk.Done, len(decoded), err, decodeErr)
	}

	// Explicit target readers remain bound to the durable session even after
	// the tab disappears.
	app.tabs = map[string]*WorkspaceTab{}
	app.tabOrder = nil
	app.activeTabID = ""
	selector := SessionSelector{Ref: &session.SessionRef{HostID: localDesktopHostID, SessionID: runtime.Ref().SessionID}}
	compatTarget, err := app.HistorySliceForTarget(selector, HistorySliceRequest{Turns: 12})
	if err != nil || len(compatTarget.Entries) != 2 || len(compatTarget.Entries[1].Refs) != 1 {
		t.Fatalf("target compatibility history = %+v, %v", compatTarget, err)
	}
	compatChunk, err := app.HistoryContentForTarget(selector, compatTarget.Entries[1].Refs[0], 0)
	if err != nil || compatChunk.Stale || !compatChunk.Done || compatChunk.Data != large {
		t.Fatalf("target compatibility content = %+v, %v", compatChunk, err)
	}
	targetPage, err := app.SessionHistoryPageForTarget(selector, "", 12)
	if err != nil || len(targetPage.Messages) != 2 {
		t.Fatalf("target history page = %+v, %v", targetPage, err)
	}
	targetLocation, err := app.LocateSessionMessageForTarget(selector, "cold-history-assistant", targetPage.SnapshotSequence)
	if err != nil || targetLocation.Status != "ready" || targetLocation.MessageID != "cold-history-assistant" {
		t.Fatalf("target location = %+v, %v", targetLocation, err)
	}
	targetRef := targetPage.Messages[1].ContentRef
	if targetRef == nil {
		t.Fatal("target large message has no content ref")
	}
	targetChunk, err := app.SessionHistoryContentForTarget(selector, *targetRef, 0)
	targetDecoded, decodeErr := base64.StdEncoding.DecodeString(targetChunk.Data)
	if err != nil || decodeErr != nil || !targetChunk.Done || !strings.Contains(string(targetDecoded), large) {
		t.Fatalf("target content = done:%v bytes:%d, errors:%v/%v", targetChunk.Done, len(targetDecoded), err, decodeErr)
	}
}

func appendSessionTestMessage(t *testing.T, runtime *session.Runtime, operationID string, message provider.Message) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"message": message})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().AppendBatch(t.Context(), operationID, []session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func appendSessionTestModel(t *testing.T, runtime *session.Runtime, operationID, modelRef string) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"modelRef": modelRef})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().AppendBatch(t.Context(), operationID, []session.Event{{Kind: "session/config", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopV3CatalogResumeRenameAndDeleteUseSessionIdentity(t *testing.T) {
	isolateDesktopUserDirs(t)
	model, targetModel := configureSwitchableDefaultModels(t)
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.ctx = context.Background()
	root := t.TempDir()
	dir := desktopSessionDir(root)
	service := app.desktopSessionService(dir)

	first, err := service.Create(t.Context(), session.CreateOptions{
		SessionID: "first-v3", CWD: root, Origin: session.SessionOriginNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	appendSessionTestModel(t, first, "first-model", model)
	appendSessionTestMessage(t, first, "first-message", provider.Message{ID: "user-first", Role: provider.RoleUser, Content: "first conversation"})
	second, err := service.Create(t.Context(), session.CreateOptions{
		SessionID: "second-v3", CWD: root, Origin: session.SessionOriginNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	appendSessionTestModel(t, second, "second-model", targetModel)
	appendSessionTestMessage(t, second, "second-message", provider.Message{ID: "user-second", Role: provider.RoleUser, Content: "second conversation"})
	for _, ref := range []session.SessionRef{first.Ref(), second.Ref()} {
		if _, err := app.attachDesktopSession(t.Context(), "project", root, ref); err != nil {
			t.Fatal(err)
		}
	}

	ctrl, err := app.buildTabControllerBoot(app.ctx, boot.Options{Model: model, WorkspaceRoot: root, SessionDir: dir, Sink: event.Discard})
	if err != nil {
		t.Fatal(err)
	}
	identity := ctrl.(control.IdentityLifecycle)
	if _, err := identity.OpenSession(t.Context(), first.Ref()); err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{ID: "v3-tab", Scope: "project", WorkspaceRoot: root, SessionID: first.Ref().SessionID, Ready: true, Ctrl: ctrl, sink: &tabEventSink{tabID: "v3-tab", app: app}, disabledMCP: map[string]ServerView{}}
	app.tabs[tab.ID] = tab
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	app.newSessionRuntimeLocked(tab, sessionRuntimeKey(tab.currentSessionIdentity()))
	t.Cleanup(func() {
		if live := app.controllerForTab(tab); live != nil {
			live.Close()
		}
	})

	rows := app.ListSessions()
	if len(rows) != 2 || rows[0].SessionID == "" || rows[1].SessionID == "" {
		t.Fatalf("v3 catalog rows = %+v", rows)
	}
	if _, err := app.ResumeSessionForTab(tab.ID, sessionRoute(second.Ref().SessionID)); err != nil {
		t.Fatal(err)
	}
	if tab.SessionID != second.Ref().SessionID || tab.SessionPath != "" {
		t.Fatalf("resumed identity = id %q path %q", tab.SessionID, tab.SessionPath)
	}
	if tab.Ctrl == ctrl || tab.Ctrl.ModelRef() != targetModel {
		t.Fatalf("target model runtime = ctrl changed %v model %q, want true/%q", tab.Ctrl != ctrl, tab.Ctrl.ModelRef(), targetModel)
	}
	if got := tab.Ctrl.History(); len(got) != 1 || got[0].Content != "second conversation" {
		t.Fatalf("resumed history = %+v", got)
	}
	if err := app.RenameSession(sessionRoute(second.Ref().SessionID), "renamed v3"); err != nil {
		t.Fatal(err)
	}
	if snap, err := service.Query().Snapshot(t.Context(), second.Ref()); err != nil || snap.Projection.Title != "renamed v3" {
		t.Fatalf("renamed snapshot = %+v, %v", snap, err)
	}
	if err := app.DeleteSession(sessionRoute(second.Ref().SessionID)); err != nil {
		t.Fatal(err)
	}
	if !tab.removed {
		t.Fatal("archived runtime remains attached")
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil || state.SessionStates[second.Ref().SessionID].Lifecycle != "archived" {
		t.Fatalf("archive state=%+v err=%v", state, err)
	}
	if _, err := service.Query().Snapshot(t.Context(), second.Ref()); err != nil {
		t.Fatalf("archive lost canonical content: %v", err)
	}
}

func TestDesktopSessionRefOpenFallsBackAndPublishesHydrationReady(t *testing.T) {
	isolateDesktopUserDirs(t)
	model, fallbackModel := configureSwitchableDefaultModels(t)
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.ctx = context.Background()
	root := t.TempDir()
	dir := desktopSessionDir(root)
	service := app.desktopSessionService(dir)

	source, err := service.Create(t.Context(), session.CreateOptions{SessionID: "resume-source"})
	if err != nil {
		t.Fatal(err)
	}
	appendSessionTestModel(t, source, "source-model", model)
	appendSessionTestMessage(t, source, "source-message", provider.Message{ID: "source-user", Role: provider.RoleUser, Content: "source remains"})
	target, err := service.Create(t.Context(), session.CreateOptions{SessionID: "resume-broken-target", CWD: root, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	appendSessionTestModel(t, target, "target-model", "missing/model")
	appendSessionTestMessage(t, target, "target-message", provider.Message{ID: "target-user", Role: provider.RoleUser, Content: "target restored"})
	if _, err := app.attachDesktopSession(t.Context(), "project", root, target.Ref()); err != nil {
		t.Fatal(err)
	}

	ctrl, err := app.buildTabControllerBoot(app.ctx, boot.Options{Model: model, WorkspaceRoot: root, SessionDir: dir, Sink: event.Discard})
	if err != nil {
		t.Fatal(err)
	}
	identity := ctrl.(control.IdentityLifecycle)
	if _, err := identity.OpenSession(t.Context(), source.Ref()); err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{ID: "source-tab", Scope: "project", WorkspaceRoot: root, SessionID: source.Ref().SessionID, Ready: true, Ctrl: ctrl, model: model, sink: &tabEventSink{tabID: "source-tab", app: app}, disabledMCP: map[string]ServerView{}}
	app.tabs[tab.ID] = tab
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	app.newSessionRuntimeLocked(tab, sessionRuntimeKey(tab.currentSessionIdentity()))
	t.Cleanup(func() {
		if live := app.controllerForTab(tab); live != nil {
			live.Close()
		}
	})

	readySignals := 0
	app.readyHook = func() { readySignals++ }
	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatal(err)
	}
	if readySignals != 1 {
		t.Fatalf("SessionRef open ready signals = %d, want 1", readySignals)
	}
	if tab.Ctrl == ctrl || tab.SessionID != target.Ref().SessionID || tab.SessionPath != "" || tab.Ctrl.ModelRef() != fallbackModel {
		t.Fatalf("fallback binding: replaced=%v session=%q path=%q model=%q", tab.Ctrl != ctrl, tab.SessionID, tab.SessionPath, tab.Ctrl.ModelRef())
	}
	if got := tab.Ctrl.History(); len(got) != 1 || got[0].Content != "target restored" {
		t.Fatalf("fallback history = %+v", got)
	}
	if snapshot, err := service.Query().Snapshot(t.Context(), target.Ref()); err != nil || snapshot.Projection.ModelRef != fallbackModel {
		t.Fatalf("fallback config was not persisted on target: %+v, %v", snapshot.Projection, err)
	}
}
