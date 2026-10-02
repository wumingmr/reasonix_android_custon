package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestPublicDeleteArchivesLegacyWithoutMovingOriginal(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.ctx = t.Context()
	pinDesktopSessionRoot(t, a)
	installNoopRuntimeEvents(a)
	t.Cleanup(a.closeSessionServices)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeLegacySession(t, dir, "public-archive.jsonl", "preserved original", time.Now())
	before, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteSession(path); err != nil {
		t.Fatal(err)
	}
	after, err := desktopSourceFingerprint(path)
	if err != nil || before != after {
		t.Fatalf("archive modified legacy original: %v", err)
	}
	page, err := a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("archive not in trash: %+v %v", page, err)
	}
	ref := page.Items[0].Ref
	if ref == nil {
		t.Fatal("canonical trash entry is missing its session reference")
	}
	if result, err := a.ApplySessionLifecycle(lifecycleRequest(t, a, *ref, "restore-public-archive", "restore")); err != nil || !result.Committed {
		t.Fatalf("restore: %+v %v", result, err)
	}
	if err := a.ArchiveCanonicalSession(*ref); err != nil {
		t.Fatal(err)
	}
	if err := a.PurgeCanonicalSession(*ref); err != nil {
		t.Fatal(err)
	}
	if err := a.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Deleted {
		t.Fatal("rescan revived purged identity")
	}
	for _, w := range state.Workspaces {
		if len(w.SessionIDs) != 0 {
			t.Fatalf("rescan recreated source under a new ID: %v", w.SessionIDs)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("upgrade original removed by purge")
	}
}

func TestPublicLegacyTrashPurgeRetainsUpgradeOriginal(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.ctx = t.Context()
	pinDesktopSessionRoot(t, a)
	installNoopRuntimeEvents(a)
	t.Cleanup(a.closeSessionServices)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeLegacySession(t, dir, "old-trash.jsonl", "retained upgrade evidence", time.Now())
	if err := deleteSessionFile(dir, path); err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(dir, sessionTrashDir, "old-trash.jsonl", "old-trash.jsonl")
	before, err := desktopSourceFingerprint(trash)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.discoverHistoricalTrash(t.Context()); err != nil {
		t.Fatal(err)
	}
	page, err := a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("legacy trash missing: %+v %v", page, err)
	}
	if page.Items[0].ArchivedAt != trashedSessionDeletedAt(trash) {
		t.Fatal("legacy archive time changed")
	}
	if err := a.PurgeTrashedSession(trash); err != nil {
		t.Fatal(err)
	}
	after, err := desktopSourceFingerprint(trash)
	if err != nil || before != after {
		t.Fatalf("original removed: %v", err)
	}
	if err := a.discoverHistoricalTrash(t.Context()); err != nil {
		t.Fatal(err)
	}
	page, err = a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("purged history revived: %+v %v", page, err)
	}
}

func lifecycleFixture(t *testing.T) (*App, session.SessionRef) {
	t.Helper()
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.ctx = t.Context()
	pinDesktopSessionRoot(t, a)
	installNoopRuntimeEvents(a)
	t.Cleanup(a.closeSessionServices)
	w, err := a.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	s := a.desktopSessionService("")
	runtime, err := s.Create(t.Context(), session.CreateOptions{SessionID: "lifecycle-fixture", CWD: globalWorkspaceRoot(), Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	appendSessionTestMessage(t, runtime, "user", provider.Message{ID: "user", Role: provider.RoleUser, Content: "retained history"})
	ref := runtime.Ref()
	if err := s.Close(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if err := a.workspaceRegistry().AttachSession(t.Context(), "", w, ref.SessionID, ""); err != nil {
		t.Fatal(err)
	}
	return a, ref
}

func addLifecycleFixtureSession(t *testing.T, a *App, id string) session.SessionRef {
	t.Helper()
	service := a.desktopSessionService("")
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: id, CWD: globalWorkspaceRoot(), Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	appendSessionTestMessage(t, runtime, "user", provider.Message{ID: "user-" + id, Role: provider.RoleUser, Content: "retained " + id})
	ref := runtime.Ref()
	if err := service.Close(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if err := a.workspaceRegistry().AttachSession(t.Context(), "", workspacestate.GlobalWorkspaceID, ref.SessionID, ""); err != nil {
		t.Fatal(err)
	}
	return ref
}

func rewriteLifecycleRegistry(t *testing.T, store *workspacestate.Store, change func(*workspacestate.State)) {
	t.Helper()
	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	change(&state)
	state.Generation++
	state.Initialized = true
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func beginLifecycleCommandForTest(t *testing.T, store *workspacestate.Store, req SessionLifecycleRequest) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if err := store.BeginCommand(t.Context(), "command-"+req.OperationID, hex.EncodeToString(sum[:]), body, req.ExpectedGeneration); err != nil {
		t.Fatal(err)
	}
}

func lifecycleRequest(t *testing.T, a *App, ref session.SessionRef, id, action string) SessionLifecycleRequest {
	t.Helper()
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return SessionLifecycleRequest{OperationID: id, Action: action, Targets: []SessionLifecycleTarget{{Ref: &ref}}, ExpectedGeneration: state.Generation}
}

func TestExplicitTargetCanonicalLifecycleDoesNotNavigate(t *testing.T) {
	a, ref := lifecycleFixture(t)
	a.tabs = map[string]*WorkspaceTab{
		"active": {ID: "active", TopicID: "unrelated", SessionID: "unrelated", Ready: true},
	}
	a.tabOrder = []string{"active"}
	a.activeTabID = "active"
	selector := SessionSelector{Ref: &ref}

	archived, err := a.ArchiveSessionTarget(selector)
	if err != nil || !archived.Committed || archived.TargetKey == "" || archived.OperationID == "" {
		t.Fatalf("ArchiveSessionTarget = %+v, %v", archived, err)
	}
	if a.activeTabID != "active" || a.tabs["active"].TopicID != "unrelated" {
		t.Fatal("archiving a cold target changed the active tab")
	}
	restored, err := a.RestoreSessionTarget(selector)
	if err != nil || !restored.Committed || restored.LifecycleGeneration <= archived.LifecycleGeneration {
		t.Fatalf("RestoreSessionTarget = %+v, %v", restored, err)
	}
	if a.activeTabID != "active" || a.tabs["active"].TopicID != "unrelated" {
		t.Fatal("restoring a cold target changed the active tab")
	}
	if _, err := a.MoveSessionTarget(selector, workspacestate.GlobalWorkspaceID, ""); err != nil {
		t.Fatalf("MoveSessionTarget: %v", err)
	}
	if _, err := a.ArchiveSessionTarget(selector); err != nil {
		t.Fatalf("archive before delete: %v", err)
	}
	deleted, err := a.DeleteSessionTarget(selector)
	if err != nil || !deleted.Committed {
		t.Fatalf("DeleteSessionTarget = %+v, %v", deleted, err)
	}
	state, loadErr := a.workspaceRegistry().Load(t.Context())
	if loadErr != nil || state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Deleted {
		t.Fatalf("deleted lifecycle = %+v, %v", state.SessionStates[ref.SessionID], loadErr)
	}
}

func TestManualCanonicalRenameRejectsStaleLifecycleSnapshot(t *testing.T) {
	a, ref := lifecycleFixture(t)
	target, err := a.resolveSessionTarget(SessionSelector{Ref: &ref})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ArchiveSessionTarget(SessionSelector{Ref: &ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RestoreSessionTarget(SessionSelector{Ref: &ref}); err != nil {
		t.Fatal(err)
	}
	if err := a.renameCanonicalSessionTarget(target, "stale title"); !errors.Is(err, workspacestate.ErrMutationConflict) {
		t.Fatalf("stale lifecycle rename error = %v, want mutation conflict", err)
	}
	info, err := a.desktopSessionService("").Query().Stat(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if info.Title == "stale title" {
		t.Fatal("stale lifecycle rename committed")
	}
}

func TestExplicitTargetLegacyArchiveAndRestoreUseCanonicalLifecycle(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.ctx = t.Context()
	pinDesktopSessionRoot(t, a)
	installNoopRuntimeEvents(a)
	t.Cleanup(a.closeSessionServices)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeLegacySession(t, dir, "target-lifecycle.jsonl", "preserve legacy source", time.Now())
	before, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	selector := SessionSelector{SessionPath: path}
	archived, err := a.ArchiveSessionTarget(selector)
	if err != nil || !archived.Committed {
		t.Fatalf("ArchiveSessionTarget legacy = %+v, %v", archived, err)
	}
	after, err := desktopSourceFingerprint(path)
	if err != nil || before != after {
		t.Fatalf("archive changed legacy source: %v", err)
	}
	restored, err := a.RestoreSessionTarget(selector)
	if err != nil || !restored.Committed {
		t.Fatalf("RestoreSessionTarget legacy = %+v, %v", restored, err)
	}
	target, err := a.resolveSessionTarget(selector)
	if err != nil || target.SessionRef.SessionID == "" || target.Lifecycle != workspacestate.Active {
		t.Fatalf("restored legacy target = %+v, %v", target, err)
	}
}

func TestExplicitTargetLegacyMoveAdoptsWithoutNavigating(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.ctx = t.Context()
	pinDesktopSessionRoot(t, a)
	installNoopRuntimeEvents(a)
	t.Cleanup(a.closeSessionServices)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeLegacySession(t, dir, "target-move.jsonl", "move legacy source", time.Now())
	before, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	a.tabs = map[string]*WorkspaceTab{"active": {ID: "active", SessionID: "unrelated"}}
	a.tabOrder = []string{"active"}
	a.activeTabID = "active"

	result, err := a.MoveSessionTarget(
		SessionSelector{SessionPath: path},
		workspacestate.GlobalWorkspaceID,
		"",
	)
	if err != nil || !result.Committed {
		t.Fatalf("MoveSessionTarget legacy = %+v, %v", result, err)
	}
	target, err := a.resolveSessionTarget(SessionSelector{SessionPath: path})
	if err != nil || target.SessionRef.SessionID == "" || target.Lifecycle != workspacestate.Active {
		t.Fatalf("moved legacy target = %+v, %v", target, err)
	}
	after, err := desktopSourceFingerprint(path)
	if err != nil || before != after {
		t.Fatalf("move changed legacy source: %v", err)
	}
	if a.activeTabID != "active" || len(a.tabs) != 1 {
		t.Fatalf("legacy move changed navigation: active=%q tabs=%d", a.activeTabID, len(a.tabs))
	}
}

func TestLifecycleCommandReceiptDoesNotReplayAfterRestore(t *testing.T) {
	a, ref := lifecycleFixture(t)
	req := lifecycleRequest(t, a, ref, "archive", "archive")
	archived, err := a.ApplySessionLifecycle(req)
	if err != nil || !archived.Committed {
		t.Fatalf("archive: %+v %v", archived, err)
	}
	page, err := a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 1 || page.Items[0].ArchivedAt == 0 {
		t.Fatalf("trash: %+v %v", page, err)
	}
	if result, err := a.ApplySessionLifecycle(lifecycleRequest(t, a, ref, "restore", "restore")); err != nil || !result.Committed {
		t.Fatalf("restore: %+v %v", result, err)
	}
	// New store instance forces a durable receipt read, rather than an RPC cache.
	a.desktopSessions.workspaceState = workspacestate.NewStore(a.workspaceRegistry().Path())
	again, err := a.ApplySessionLifecycle(req)
	if err != nil || again.Generation != archived.Generation {
		t.Fatalf("replay: %+v %v", again, err)
	}
	state, _ := a.workspaceRegistry().Load(t.Context())
	if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Active {
		t.Fatal("old archive request archived restored session")
	}
	req.Action = "purge"
	if _, err := a.ApplySessionLifecycle(req); err == nil {
		t.Fatal("reused operation accepted different request")
	}
	history, err := a.desktopSessionService("").Query().History(t.Context(), ref)
	if err != nil || len(history) == 0 {
		t.Fatalf("history lost: %v", err)
	}
}

func TestLifecycleCommandPersistsTerminalConflict(t *testing.T) {
	a, ref := lifecycleFixture(t)
	store := a.workspaceRegistry()
	if err := a.ArchiveCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	req := SessionLifecycleRequest{
		OperationID:        "terminal-conflict",
		Action:             "purge",
		Targets:            []SessionLifecycleTarget{{Ref: &ref}},
		ExpectedGeneration: state.Generation,
	}
	beginLifecycleCommandForTest(t, store, req)
	if err := store.RestoreSession(t.Context(), ref.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveSession(t.Context(), ref.SessionID); err != nil {
		t.Fatal(err)
	}

	first, err := a.ApplySessionLifecycle(req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Committed || len(first.Items) != 1 || first.Items[0].ErrorCode != "state_conflict" || first.Items[0].Retryable {
		t.Fatalf("terminal conflict result = %+v", first)
	}
	state, err = store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingOperations["command-terminal-conflict"].Phase != "committed" {
		t.Fatalf("terminal failure was not finalized: %+v", state.PendingOperations["command-terminal-conflict"])
	}

	second, err := a.ApplySessionLifecycle(req)
	if err != nil {
		t.Fatal(err)
	}
	firstBody, _ := json.Marshal(first)
	secondBody, _ := json.Marshal(second)
	if string(firstBody) != string(secondBody) {
		t.Fatalf("terminal retry changed result: first=%s second=%s", firstBody, secondBody)
	}
	history, err := a.desktopSessionService("").Query().History(t.Context(), ref)
	if err != nil || len(history) == 0 {
		t.Fatalf("terminal conflict changed history: messages=%d err=%v", len(history), err)
	}
}

func TestLifecycleCommandRetriesOnlyRetryableTargets(t *testing.T) {
	a, succeededRef := lifecycleFixture(t)
	conflictRef := addLifecycleFixtureSession(t, a, "lifecycle-conflict")
	missingRef := session.SessionRef{HostID: localDesktopHostID, SessionID: "lifecycle-missing"}
	store := a.workspaceRegistry()
	for _, ref := range []session.SessionRef{succeededRef, conflictRef} {
		if err := a.ArchiveCanonicalSession(ref); err != nil {
			t.Fatal(err)
		}
	}
	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	req := SessionLifecycleRequest{
		OperationID: "mixed-retry",
		Action:      "purge",
		Targets: []SessionLifecycleTarget{
			{Ref: &succeededRef},
			{Ref: &conflictRef},
			{Ref: &missingRef},
		},
		ExpectedGeneration: state.Generation,
	}
	beginLifecycleCommandForTest(t, store, req)
	if err := store.RestoreSession(t.Context(), conflictRef.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveSession(t.Context(), conflictRef.SessionID); err != nil {
		t.Fatal(err)
	}

	first, err := a.ApplySessionLifecycle(req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Committed || len(first.Items) != 3 || !first.Items[0].Committed || first.Items[1].ErrorCode != "state_conflict" || first.Items[1].Retryable || !first.Items[2].Retryable {
		t.Fatalf("mixed first result = %+v", first)
	}
	state, err = store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingOperations["command-mixed-retry"].Phase == "committed" {
		t.Fatal("retryable mixed command finalized early")
	}
	// Make rerunning the successful child observable: without the saved child
	// result, the deleted identity no longer has a purge operation to resume.
	rewriteLifecycleRegistry(t, store, func(state *workspacestate.State) {
		delete(state.PendingOperations, "purge-"+succeededRef.SessionID)
	})

	second, err := a.ApplySessionLifecycle(req)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Items[0].Committed || second.Items[1].ErrorCode != "state_conflict" || !second.Items[2].Retryable {
		t.Fatalf("mixed retry result = %+v", second)
	}
	state, err = store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, recreated := state.PendingOperations["purge-"+succeededRef.SessionID]; recreated {
		t.Fatal("successful target was executed again")
	}
}

func TestLegacyPreparedPurgeCannotDeleteRestoredSession(t *testing.T) {
	a, ref := lifecycleFixture(t)
	store := a.workspaceRegistry()
	if err := a.ArchiveCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	status := state.SessionStates[ref.SessionID]
	legacy := workspacestate.Operation{
		ID:                 "purge-" + ref.SessionID,
		Kind:               "purge",
		Phase:              "prepared",
		Lifecycle:          workspacestate.Deleted,
		SessionIDs:         []string{ref.SessionID},
		ExpectedGeneration: status.Generation,
	}
	rewriteLifecycleRegistry(t, store, func(state *workspacestate.State) {
		state.PendingOperations[legacy.ID] = legacy
	})

	restored, err := a.ApplySessionLifecycle(lifecycleRequest(t, a, ref, "restore-legacy-prepared", "restore"))
	if err != nil || !restored.Committed {
		t.Fatalf("restore legacy prepared: %+v %v", restored, err)
	}
	if err := a.resumeCanonicalPurge(t.Context(), ref, legacy); !errors.Is(err, workspacestate.ErrMutationConflict) {
		t.Fatalf("obsolete purge replay = %v, want conflict", err)
	}
	state, err = store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Active {
		t.Fatalf("obsolete purge changed lifecycle: %+v", state.SessionStates[ref.SessionID])
	}
	if _, exists := state.PendingOperations[legacy.ID]; exists {
		t.Fatal("restore left obsolete prepared purge")
	}
	history, err := a.desktopSessionService("").Query().History(t.Context(), ref)
	if err != nil || len(history) == 0 {
		t.Fatalf("restored history lost: messages=%d err=%v", len(history), err)
	}
	if err := a.ArchiveCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	if err := a.PurgeCanonicalSession(ref); err != nil {
		t.Fatalf("new explicit purge after restore: %v", err)
	}
}

func TestStartupRecoveryCleansStalePreparedWithoutDeletingContent(t *testing.T) {
	a, ref := lifecycleFixture(t)
	store := a.workspaceRegistry()
	if err := a.ArchiveCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	legacy := workspacestate.Operation{
		ID:                 "purge-" + ref.SessionID,
		Kind:               "purge",
		Phase:              "prepared",
		Lifecycle:          workspacestate.Deleted,
		SessionIDs:         []string{ref.SessionID},
		ExpectedGeneration: state.SessionStates[ref.SessionID].Generation,
	}
	if err := store.RestoreSession(t.Context(), ref.SessionID); err != nil {
		t.Fatal(err)
	}
	rewriteLifecycleRegistry(t, store, func(state *workspacestate.State) {
		state.PendingOperations[legacy.ID] = legacy
	})

	if err := a.recoverDesktopSessionOperations(t.Context()); !errors.Is(err, workspacestate.ErrMutationConflict) {
		t.Fatalf("stale startup recovery = %v, want recorded conflict", err)
	}
	state, err = store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := state.PendingOperations[legacy.ID]; exists {
		t.Fatal("startup recovery left stale prepared purge")
	}
	if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Active {
		t.Fatalf("startup recovery changed lifecycle: %+v", state.SessionStates[ref.SessionID])
	}
	history, err := a.desktopSessionService("").Query().History(t.Context(), ref)
	if err != nil || len(history) == 0 {
		t.Fatalf("startup recovery deleted content: messages=%d err=%v", len(history), err)
	}
}

func TestPurgeInterruptedTombstoneRemainsActionableAndResumes(t *testing.T) {
	a, ref := lifecycleFixture(t)
	ctx := t.Context()
	store := a.workspaceRegistry()
	if err := a.ArchiveCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginPurge(ctx, ref.SessionID, state.Generation); err != nil {
		t.Fatal(err)
	}
	page, err := a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 1 || page.Items[0].CanRestore || page.Items[0].OperationPhase != "tombstoned" {
		t.Fatalf("pending purge disappeared: %+v %v", page, err)
	}
	if _, err := a.OpenSession(ref); err == nil {
		t.Fatal("tombstoned session opened")
	}
	if err := a.recoverDesktopSessionOperations(ctx); err != nil {
		t.Fatal(err)
	}
	deleted, err := a.DeleteSessionTarget(SessionSelector{Ref: &ref})
	if err != nil || !deleted.Committed {
		t.Fatalf("resume explicit delete: %+v %v", deleted, err)
	}
	page, err = a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("purge not completed: %+v %v", page, err)
	}
	state, _ = store.Load(ctx)
	if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Deleted {
		t.Fatal("tombstone lost")
	}
	if deleted.LifecycleGeneration != state.SessionStates[ref.SessionID].Generation {
		t.Fatalf("deleted generation = %d, want %d", deleted.LifecycleGeneration, state.SessionStates[ref.SessionID].Generation)
	}
	if _, err := os.Stat(filepath.Join(a.desktopSessions.root, ref.SessionID)); !os.IsNotExist(err) {
		t.Fatalf("body remains: %v", err)
	}
	if err := store.AttachSession(ctx, "", "global", ref.SessionID, ""); err == nil {
		t.Fatal("deleted session revived")
	}
}

func TestMigrationNestedEvidenceUnknownFields(t *testing.T) {
	var receipt desktopMigrationReceipt
	var conversion desktopMigrationConversion
	if err := json.Unmarshal([]byte(`{"targetSessionId":"s","future":{"x":1}}`), &receipt); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"root":"r","sessionId":"s","codec":"c","future":{"x":1}}`), &conversion); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{receipt, conversion} {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		if string(fields["future"]) != `{"x":1}` {
			t.Fatalf("lost evidence: %s", body)
		}
	}
}
