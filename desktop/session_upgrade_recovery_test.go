package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestCanonicalPinnedShellUsesRegistryAndArchiveLifecycle(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = t.Context()
	installNoopRuntimeEvents(app)
	pinDesktopSessionRoot(t, app)
	t.Cleanup(app.closeSessionServices)
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: "pin-fixture", CWD: globalWorkspaceRoot(), Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspaceID, runtime.Ref().SessionID, ""); err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{ID: "fixture-tab", Scope: "global", SessionID: runtime.Ref().SessionID, SessionWorkspace: desktopTabWorkspace{ID: workspaceID}}
	app.tabs[tab.ID] = tab
	if err := app.ensureTabTopicIndexedForUserTurn(tab); err != nil {
		t.Fatal(err)
	}
	if err := app.RenameCanonicalSession(runtime.Ref(), "Canonical title"); err != nil {
		t.Fatal(err)
	}
	if err := app.SetTopicPinned(tab.TopicID, true); err != nil {
		t.Fatal(err)
	}
	assertPins := func(want int) {
		t.Helper()
		snapshot := mustProjectTreeSnapshot(t, app)
		pins := []ProjectNode{}
		for _, project := range snapshot.Projects {
			pins = append(pins, project.Children...)
		}
		if len(pins) != want {
			t.Fatalf("pins = %+v, want %d", pins, want)
		}
		if want == 1 && (pins[0].Label != "Canonical title" || pins[0].Session == nil || pins[0].Session.SessionID != runtime.Ref().SessionID || !pins[0].Pinned) {
			t.Fatalf("stale pinned shell: %+v", pins[0])
		}
	}
	assertPins(1)
	if err := app.workspaceRegistry().ArchiveSession(t.Context(), runtime.Ref().SessionID); err != nil {
		t.Fatal(err)
	}
	assertPins(0)
	if err := app.workspaceRegistry().RestoreSession(t.Context(), runtime.Ref().SessionID); err != nil {
		t.Fatal(err)
	}
	assertPins(1)
}

func TestHistoricalDAGHeadsRestoreIndependentlyWithoutChangingOriginal(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "branches.jsonl")
	legacy := agent.NewSession("system")
	legacy.Add(provider.Message{Role: provider.RoleUser, Content: "shared"})
	legacy.Add(provider.Message{Role: provider.RoleAssistant, Content: "original answer"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ForkHead(path, legacy.Snapshot()[1].ID, agent.HeadKindFork, "alternate"); err != nil {
		t.Fatal(err)
	}
	legacy.Add(provider.Message{Role: provider.RoleAssistant, Content: "alternate answer"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	before, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx = t.Context()
	pinDesktopSessionRoot(t, app)
	installNoopRuntimeEvents(app)
	if err := app.discoverLegacyHeads(t.Context(), path, "legacy", "global", ""); err != nil {
		t.Fatal(err)
	}
	page, err := app.ListRecoveryEntries("", "", 50)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("heads=%+v err=%v", page, err)
	}
	preview, err := app.PreviewRecoveryEntry(page.Items[0].ID)
	if err != nil || len(preview.Messages) == 0 {
		t.Fatalf("preview=%+v %v", preview, err)
	}
	result, err := app.RestoreRecoveryEntry(page.Items[0].ID, "head-restore")
	if err != nil {
		t.Fatal(err)
	}
	history, err := app.desktopSessionService("").Query().History(t.Context(), result.Session)
	if err != nil || len(history) != 3 || history[2].Content != "original answer" {
		t.Fatalf("wrong head: %+v %v", history, err)
	}
	after, err := desktopSourceFingerprint(path)
	if err != nil || before != after {
		t.Fatalf("DAG source changed: %v", err)
	}
}

func assertLegacyLifecycle(t *testing.T, app *App, path, lifecycle string) session.SessionRef {
	t.Helper()
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mapping, exists := state.SourceMappings[desktopSourceKey(path, "")]
	if !exists || state.SessionStates[mapping.SessionID].Lifecycle != lifecycle {
		t.Fatalf("source lifecycle mapping=%+v state=%+v", mapping, state.SessionStates[mapping.SessionID])
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("legacy original was not preserved: %v", err)
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}
	if _, err := app.desktopSessionService("").Query().Snapshot(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestArchiveRestartRestoreRestartAndInterruptedReplay(t *testing.T) {
	for _, phase := range []string{"prepared", "content_ready"} {
		t.Run(phase, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			dir := config.SessionDir()
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := writeLegacySession(t, dir, "replay.jsonl", "durable history", time.Now())
			app := NewApp()
			app.ctx = t.Context()
			pinDesktopSessionRoot(t, app)
			installNoopRuntimeEvents(app)
			workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := app.migrateLegacySession(t.Context(), path, desktopMigrationSource{scope: "global"}, workspaceID); err != nil {
				t.Fatal(err)
			}
			ref := assertLegacyLifecycle(t, app, path, workspacestate.Active)
			op := workspacestate.Operation{ID: "crash-archive", Kind: "archive", SessionIDs: []string{ref.SessionID}, Lifecycle: workspacestate.Archived}
			if err := app.workspaceRegistry().BeginOperation(t.Context(), op); err != nil {
				t.Fatal(err)
			}
			if phase == "content_ready" {
				if err := app.workspaceRegistry().PrepareOperationContent(t.Context(), op.ID, op.SessionIDs, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			root := app.desktopSessions.root
			app.closeSessionServices()
			restart := func() *App {
				a := NewApp()
				a.ctx = t.Context()
				a.desktopSessions.root = root
				installNoopRuntimeEvents(a)
				t.Cleanup(a.closeSessionServices)
				return a
			}
			app = restart()
			release, err := session.NewFilesystemPersistence(root).AcquireMaintenance(ref.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if err := app.recoverDesktopSessionOperations(t.Context()); err == nil {
				t.Fatal("replay ignored external writer")
			}
			assertLegacyLifecycle(t, app, path, workspacestate.Active)
			release()
			if err := app.recoverDesktopSessionOperations(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertLegacyLifecycle(t, app, path, workspacestate.Archived)
			page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
			if err != nil || len(page.Items) != 0 {
				t.Fatalf("archived sidebar: %+v %v", page, err)
			}
			if err := app.RestoreCanonicalSession(ref); err != nil {
				t.Fatal(err)
			}
			app.closeSessionServices()
			app = restart()
			assertLegacyLifecycle(t, app, path, workspacestate.Active)
			page, err = app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
			if err != nil || len(page.Items) != 1 || page.Items[0].Session == nil || *page.Items[0].Session != ref {
				t.Fatalf("restored sidebar: %+v %v", page, err)
			}
		})
	}
}

func TestHistoricalSourceFingerprintIgnoresMutableDisplayMetadata(t *testing.T) {
	dir := t.TempDir()
	path := writeLegacySession(t, dir, "source.jsonl", "same content", time.Now())
	before, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".meta", []byte(`{"topic_title":"Renamed","preview":"Indexed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := desktopSourceFingerprint(path)
	if err != nil || before != after {
		t.Fatalf("mutable projection changed source identity: %v", err)
	}
}

func TestHistoricalIdentityDoesNotMergeEqualMessagesOrChangedSources(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = t.Context()
	pinDesktopSessionRoot(t, app)
	workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{writeLegacySession(t, t.TempDir(), "same.jsonl", "identical", time.Now()), writeLegacySession(t, t.TempDir(), "same.jsonl", "identical", time.Now())}
	refs := []session.SessionRef{}
	for _, path := range paths {
		if err := app.migrateLegacySession(t.Context(), path, desktopMigrationSource{scope: "global"}, workspace); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, assertLegacyLifecycle(t, app, path, workspacestate.Active))
	}
	if refs[0] == refs[1] {
		t.Fatal("different files with equal messages were merged")
	}
	if err := os.WriteFile(paths[0], []byte("{\"role\":\"user\",\"content\":\"changed old source\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if ref, found, err := app.legacyCanonicalRef(t.Context(), paths[0]); err != nil || !found || ref != refs[0] {
		t.Fatalf("opening adopted history must remain independent of its retained source: %v %v", ref, err)
	}
	// An explicit source re-evaluation still quarantines changed history; an
	// ordinary open no longer performs this expensive scan or mutates recovery.
	if err := app.migrateLegacySession(t.Context(), paths[0], desktopMigrationSource{scope: "global"}, workspace); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Workspaces[workspace].SessionIDs) != 2 || len(state.RecoveryEntries) != 1 {
		t.Fatalf("changed source overwrote or duplicated live sessions: %+v", state)
	}
	history, err := app.desktopSessionService("").Query().History(t.Context(), refs[0])
	if err != nil || len(history) != 1 || history[0].Content != "identical" {
		t.Fatalf("import overwritten: %+v %v", history, err)
	}
}

func TestHistoricalWorkspaceConflictRetainsSourceWithoutRegistration(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = t.Context()
	pinDesktopSessionRoot(t, app)
	workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	path := writeLegacySession(t, t.TempDir(), "foreign.jsonl", "preserve", time.Now())
	body, _ := json.Marshal(map[string]string{"workspace_root": t.TempDir()})
	if err := os.WriteFile(path+".meta", body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.migrateLegacySession(t.Context(), path, desktopMigrationSource{scope: "global"}, workspace); err == nil {
		t.Fatal("conflicting workspace imported")
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil || len(state.Workspaces[workspace].SessionIDs) != 0 || len(state.RecoveryEntries) != 1 {
		t.Fatalf("conflict was not isolated: %+v %v", state, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalTrashRestorePreservesSourceAndSurvivesRestart(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := writeLegacySession(t, dir, "old.jsonl", "keep historical content", time.Now())
	if err := trashSessionArtifacts(dir, path, filepath.Base(path)); err != nil {
		t.Fatal(err)
	}
	trashPath := filepath.Join(sessionTrashPath(dir), filepath.Base(path), filepath.Base(path))
	// Historical versions recorded no reliable archive/delete distinction.
	if err := os.WriteFile(filepath.Join(filepath.Dir(trashPath), sessionTrashMetaFile), []byte(`{"key":"old.jsonl","deletedAt":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(trashPath)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx = t.Context()
	pinDesktopSessionRoot(t, app)
	installNoopRuntimeEvents(app)
	if err := app.discoverHistoricalTrash(t.Context()); err != nil {
		t.Fatal(err)
	}
	page, err := app.ListRecoveryEntries("", "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("discover: %+v %v", page, err)
	}
	result, err := app.RestoreRecoveryEntry(page.Items[0].ID, "restore-fixture")
	if err != nil {
		t.Fatal(err)
	}
	again, err := app.RestoreRecoveryEntry(page.Items[0].ID, "restore-fixture")
	if err != nil || again != result {
		t.Fatalf("repeat restore=%+v %v", again, err)
	}
	after, err := os.ReadFile(trashPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("restore changed legacy source")
	}
	history, err := app.ReadSessionHistory(result.Session, "", 32)
	if err != nil || len(history.Messages) == 0 {
		t.Fatalf("restored history: %+v %v", history, err)
	}
	root := app.desktopSessions.root
	app.closeSessionServices()
	restarted := NewApp()
	restarted.ctx = t.Context()
	restarted.desktopSessions.root = root
	t.Cleanup(restarted.closeSessionServices)
	installNoopRuntimeEvents(restarted)
	if err := restarted.discoverHistoricalTrash(t.Context()); err != nil {
		t.Fatal(err)
	}
	hidden, err := restarted.ListRecoveryEntries("", "", 10)
	if err != nil || len(hidden.Items) != 0 {
		t.Fatalf("restored entry resurrected: %+v %v", hidden, err)
	}
	visible, err := restarted.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 10})
	if err != nil || len(visible.Items) != 1 || visible.Items[0].Session == nil || *visible.Items[0].Session != result.Session {
		t.Fatalf("restart sidebar=%+v %v", visible, err)
	}
	// A committed operation remains replayable even when removable historical
	// media is no longer available. Its durable result owns the retry.
	if err := os.Rename(trashPath, trashPath+".offline"); err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.RestoreRecoveryEntry(page.Items[0].ID, "restore-fixture")
	if err != nil || replayed != result {
		t.Fatalf("offline committed retry=%+v %v", replayed, err)
	}
}

func TestRepeatedLegacyMigrationDoesNotReimportOldSnapshot(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := writeLegacySession(t, dir, "source.jsonl", "original", time.Now())
	app := NewApp()
	pinDesktopSessionRoot(t, app)
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	source := desktopMigrationSource{scope: "global"}
	if err := app.migrateLegacySession(t.Context(), path, source, workspaceID); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	id := state.SourceMappings[desktopSourceKey(path, "")].SessionID
	service := app.desktopSessionService("")
	binding, err := service.Open(t.Context(), session.SessionRef{HostID: localDesktopHostID, SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := service.Runtime(session.SessionRef{HostID: localDesktopHostID, SessionID: id})
	payload, _ := json.Marshal(map[string]any{"message": map[string]any{"id": "new-user", "role": "user", "content": "new canonical work"}})
	if _, err := runtime.Session().AppendBatch(t.Context(), "new-work", []session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := binding.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().ArchiveSession(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := app.migrateLegacySession(t.Context(), path, source, workspaceID); err != nil {
		t.Fatal(err)
	}
	state, err = app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Workspaces[workspaceID].SessionIDs) != 1 || state.SessionStates[id].Lifecycle != workspacestate.Archived {
		t.Fatal("rescan duplicated or unarchived the continued session")
	}
	messages, err := service.Query().History(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if messages[len(messages)-1].Content != "new canonical work" {
		t.Fatal("rescan replaced newer canonical history")
	}
}

func TestRecoveryAPIsRejectUnregisteredPaths(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	pinDesktopSessionRoot(t, app)
	for _, id := range []string{"/etc/passwd", "../outside", "legacy-unknown"} {
		if _, err := app.PreviewRecoveryEntry(id); err == nil {
			t.Fatalf("preview accepted %q", id)
		}
		if _, err := app.RestoreRecoveryEntry(id, "op"); err == nil {
			t.Fatalf("restore accepted %q", id)
		}
	}
}
