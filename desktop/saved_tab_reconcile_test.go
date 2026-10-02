package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/desktop/internal/draftstate"
	"reasonix/desktop/internal/legacycleanup"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/session"
)

func newSavedTabReconcileTestApp(t *testing.T) *App {
	t.Helper()
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = t.Context()
	pinDesktopSessionRoot(t, app)
	installNoopRuntimeEvents(app)
	app.legacyCleanup = legacycleanup.New(filepath.Join(t.TempDir(), "legacy-empty-session-cleanup-v1.json"))
	t.Cleanup(func() { _ = app.draftStore().Close() })
	return app
}

func finishSavedTabMigration(app *App) {
	close(app.desktopMigrationDone)
}

func savedProjectTab(id, sessionID, operationID, root, workspaceID string) desktopTabEntry {
	return desktopTabEntry{
		ID: id, Scope: "project", WorkspaceRoot: root, WorkspaceID: workspaceID,
		TopicID: "topic-" + id, SessionID: sessionID, CreateOperationID: operationID,
	}
}

func TestReconcileSavedTabsWaitsForMigrationCreatedCanonicalSession(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	root := t.TempDir()
	workspaceID := desktopWorkspaceID("project", root)
	file := desktopTabsFile{Tabs: []desktopTabEntry{savedProjectTab("saved", "migrated-session", "", root, workspaceID)}, ActiveTab: "saved"}

	type result struct {
		file    desktopTabsFile
		changed bool
	}
	finished := make(chan result, 1)
	reachedMigration := make(chan struct{})
	app.beforeSavedTabMigrationWait = func() { close(reachedMigration) }
	go func() {
		got, changed := app.reconcileSavedTabs(t.Context(), file)
		finished <- result{got, changed}
	}()
	<-reachedMigration
	select {
	case <-finished:
		t.Fatal("saved tab reconciliation passed a missing canonical identity before migration completed")
	default:
	}

	ref, gotWorkspaceID := createLegacyCleanupSession(t, app, root, "migrated-session", true)
	if ref.SessionID != "migrated-session" || gotWorkspaceID != workspaceID {
		t.Fatalf("migration fixture identity = %q/%q", ref.SessionID, gotWorkspaceID)
	}
	finishSavedTabMigration(app)
	got := <-finished
	if got.changed || len(got.file.Tabs) != 1 || got.file.Tabs[0].SessionID != ref.SessionID {
		t.Fatalf("migration-created session was not restored: changed=%v file=%+v", got.changed, got.file)
	}
}

func TestReconcileSavedTabsDropsMissingCanonicalWithoutOwner(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	finishSavedTabMigration(app)
	root := t.TempDir()
	file := desktopTabsFile{
		Tabs:      []desktopTabEntry{savedProjectTab("stale", "missing-session", "", root, desktopWorkspaceID("project", root))},
		ActiveTab: "stale", TabOrder: []string{"stale"},
	}

	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if !changed || len(got.Tabs) != 0 || got.ActiveTab != "" || len(got.TabOrder) != 0 {
		t.Fatalf("stale tab reconciliation = changed:%v file:%+v", changed, got)
	}
}

func TestReconcileSavedTabsSelectsValidFallbackAfterDroppingActive(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	finishSavedTabMigration(app)
	root := t.TempDir()
	file := desktopTabsFile{
		Tabs:       []desktopTabEntry{savedProjectTab("stale", "missing-session", "", root, desktopWorkspaceID("project", root))},
		RemoteTabs: []desktopRemoteTabEntry{{ID: "remote", HostID: "host", Workspace: "/workspace"}},
		ActiveTab:  "stale", RemoteTabOrder: []string{"remote"}, TabOrder: []string{"stale", "remote"},
	}

	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if !changed || len(got.Tabs) != 0 || len(got.RemoteTabs) != 1 || got.ActiveTab != "remote" {
		t.Fatalf("fallback selection = changed:%v file:%+v", changed, got)
	}
	single := singleSurfaceTabsFile(got)
	if len(single.RemoteTabs) != 1 || single.RemoteTabs[0].ID != "remote" {
		t.Fatalf("single-surface fallback discarded valid remote tab: %+v", single)
	}
}

func TestReconcileSavedTabsDropsMissingLegacyPathWithoutOwner(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	finishSavedTabMigration(app)
	missing := filepath.Join(t.TempDir(), "missing-session.jsonl")
	file := desktopTabsFile{Tabs: []desktopTabEntry{{ID: "legacy", Scope: "global", TopicID: "topic", SessionPath: missing}}, ActiveTab: "legacy"}

	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if !changed || len(got.Tabs) != 0 {
		t.Fatalf("missing legacy tab reconciliation = changed:%v file:%+v", changed, got)
	}
}

func TestReconcileSavedTabsDiagnosticDoesNotExposeIdentityOrPath(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	finishSavedTabMigration(app)
	secretPath := filepath.Join(t.TempDir(), "private-customer-path", "missing.jsonl")
	const secretSessionID = "private-session-identity"
	file := desktopTabsFile{Tabs: []desktopTabEntry{{
		ID: "private-tab", Scope: "global", TopicID: "topic", SessionID: secretSessionID, SessionPath: secretPath,
	}}, ActiveTab: "private-tab"}
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	if _, changed := app.reconcileSavedTabs(t.Context(), file); !changed {
		t.Fatal("missing saved tab was not reconciled")
	}
	logBody := output.String()
	if strings.Contains(logBody, secretPath) || strings.Contains(logBody, secretSessionID) || strings.Contains(logBody, "private-tab") {
		t.Fatalf("saved-tab diagnostic exposed private identity: %s", logBody)
	}
	for _, field := range []string{"outcome=drop_stale_presentation", "reason=canonical_identity_absent", "identity_kind=canonical", "waited_for_migration=true"} {
		if !strings.Contains(logBody, field) {
			t.Fatalf("saved-tab diagnostic missing %q: %s", field, logBody)
		}
	}
}

func TestReconcileSavedTabsPreservesRecoveryEntry(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	root := t.TempDir()
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().RecordRecovery(t.Context(), workspacestate.RecoveryEntry{
		ID: "recovery", SourceKey: "recovery-source", SessionID: "missing-with-recovery", WorkspaceID: workspaceID,
		Scope: "project", WorkspaceRoot: root, Format: "canonical", Reason: "interrupted", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	finishSavedTabMigration(app)
	file := desktopTabsFile{Tabs: []desktopTabEntry{savedProjectTab("recover", "missing-with-recovery", "", root, workspaceID)}, ActiveTab: "recover"}

	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if changed || len(got.Tabs) != 1 {
		t.Fatalf("recovery-owned tab was discarded: changed=%v file=%+v", changed, got)
	}
}

func TestReconcileSavedTabsRetainsMatchingPendingCreateWithoutWaiting(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	root := t.TempDir()
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "pending-session"
	const operationID = "pending-operation"
	if err := app.workspaceRegistry().BeginCreate(t.Context(), workspacestate.PendingCreate{
		OperationID: operationID, WorkspaceID: workspaceID, SessionID: sessionID,
	}); err != nil {
		t.Fatal(err)
	}
	file := desktopTabsFile{Tabs: []desktopTabEntry{savedProjectTab("pending", sessionID, operationID, root, workspaceID)}, ActiveTab: "pending"}

	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if changed || len(got.Tabs) != 1 || got.Tabs[0].CreateOperationID != operationID {
		t.Fatalf("pending create was not retained: changed=%v file=%+v", changed, got)
	}
}

func TestReconcileSavedTabsRestoresSessionIdentityFromPendingCreate(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	root := t.TempDir()
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "pending-session"
	const operationID = "pending-operation"
	if err := app.workspaceRegistry().BeginCreate(t.Context(), workspacestate.PendingCreate{
		OperationID: operationID, WorkspaceID: workspaceID, SessionID: sessionID,
	}); err != nil {
		t.Fatal(err)
	}
	file := desktopTabsFile{Tabs: []desktopTabEntry{{
		ID: "pending", Scope: "project", WorkspaceRoot: root, WorkspaceID: workspaceID, CreateOperationID: operationID,
	}}, ActiveTab: "pending"}

	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if !changed || len(got.Tabs) != 1 || got.Tabs[0].SessionID != sessionID {
		t.Fatalf("pending identity was not restored: changed=%v file=%+v", changed, got)
	}
}

func TestReconcileSavedTabsDropsArchivedPresentationButKeepsSession(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	root := t.TempDir()
	ref, workspaceID := createLegacyCleanupSession(t, app, root, "archived-session", true)
	if err := app.archiveSessionRefsWithOperation([]session.SessionRef{ref}, "archive-test"); err != nil {
		t.Fatal(err)
	}
	file := desktopTabsFile{Tabs: []desktopTabEntry{savedProjectTab("archived", ref.SessionID, "", root, workspaceID)}, ActiveTab: "archived"}

	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if !changed || len(got.Tabs) != 0 {
		t.Fatalf("archived presentation retained: changed=%v file=%+v", changed, got)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Archived {
		t.Fatalf("archived session lifecycle = %q", state.SessionStates[ref.SessionID].Lifecycle)
	}
	if _, err := app.desktopSessionService("").Query().Stat(t.Context(), ref); err != nil {
		t.Fatalf("archived session content was removed: %v", err)
	}
}

func TestReconcileSavedTabsPreservesUnregisteredCanonicalContent(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	root := t.TempDir()
	runtime, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{
		SessionID: "unregistered-content", CWD: root, Origin: session.SessionOriginNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := app.desktopSessionService("").Close(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}
	finishSavedTabMigration(app)
	file := desktopTabsFile{Tabs: []desktopTabEntry{savedProjectTab("recover", runtime.Ref().SessionID, "", root, desktopWorkspaceID("project", root))}, ActiveTab: "recover"}

	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if changed || len(got.Tabs) != 1 {
		t.Fatalf("unregistered canonical content was discarded: changed=%v file=%+v", changed, got)
	}
}

func TestReconcileSavedTabsPreservesEmptyWorkspaceConflictWithoutRetiredDraftReads(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	canonicalRoot := t.TempDir()
	ref, workspaceID := createLegacyCleanupSession(t, app, canonicalRoot, "empty-workspace-conflict", false)
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	workspace := state.Workspaces[workspaceID]
	workspace.Root = t.TempDir()
	state.Workspaces[workspaceID] = workspace
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app.workspaceRegistry().Path(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	finishSavedTabMigration(app)
	file := desktopTabsFile{
		Tabs:       []desktopTabEntry{savedProjectTab("empty", ref.SessionID, "", canonicalRoot, workspaceID)},
		RemoteTabs: []desktopRemoteTabEntry{{ID: "remote", HostID: "host", Workspace: "/workspace"}},
		ActiveTab:  "remote", TabOrder: []string{"remote", "empty"},
	}
	body = mustMarshalJSON(t, file)
	if err := os.MkdirAll(desktopConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(desktopConfigDir(), tabsFileName), body, 0o600); err != nil {
		t.Fatal(err)
	}

	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if changed || len(got.Tabs) != 1 || len(got.RemoteTabs) != 1 || !got.Tabs[0].restoreBlocked {
		t.Fatalf("formal workspace conflict was not retained for repair: changed=%v file=%+v", changed, got)
	}
	if persisted := loadTabsFile(); len(persisted.RemoteTabs) != 1 {
		t.Fatalf("preflight archive overwrote unpublished tabs: %+v", persisted)
	}
	state, err = app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Active {
		t.Fatalf("empty formal session lifecycle = %q, want active", state.SessionStates[ref.SessionID].Lifecycle)
	}
	if _, err := os.Stat(app.draftStore().Path()); !os.IsNotExist(err) {
		t.Fatalf("workspace conflict opened the retired draft database: %v", err)
	}
	if len(app.tabs) != 0 || len(app.runtimeByID) != 0 {
		t.Fatal("empty-session reconciliation created a replacement runtime")
	}
}

func TestReconcileSavedTabsPreservesNonEmptyWorkspaceConflict(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	canonicalRoot := t.TempDir()
	ref, workspaceID := createLegacyCleanupSession(t, app, canonicalRoot, "used-workspace-conflict", true)
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	workspace := state.Workspaces[workspaceID]
	workspace.Root = t.TempDir()
	state.Workspaces[workspaceID] = workspace
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app.workspaceRegistry().Path(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	finishSavedTabMigration(app)
	file := desktopTabsFile{Tabs: []desktopTabEntry{savedProjectTab("used", ref.SessionID, "", canonicalRoot, workspaceID)}, ActiveTab: "used"}

	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if changed || len(got.Tabs) != 1 {
		t.Fatalf("non-empty stale-workspace session was discarded: changed=%v file=%+v", changed, got)
	}
	state, err = app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Active {
		t.Fatalf("non-empty session lifecycle = %q, want active", state.SessionStates[ref.SessionID].Lifecycle)
	}
}

func TestRestoreDropsStaleSavedTabWithoutReplacementRuntime(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	finishSavedTabMigration(app)
	root := t.TempDir()
	file := desktopTabsFile{
		Tabs:      []desktopTabEntry{savedProjectTab("stale", "gone", "", root, desktopWorkspaceID("project", root))},
		ActiveTab: "stale", TabOrder: []string{"stale"},
	}
	body, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(desktopConfigDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(desktopConfigDir(), tabsFileName), body, 0o600); err != nil {
		t.Fatal(err)
	}
	app.tabsRestored = make(chan struct{})

	app.restoreOrBuildTabs()
	if len(app.tabs) != 0 || len(app.runtimeByID) != 0 || len(app.runtimeBySessionKey) != 0 {
		t.Fatalf("stale restore created runtime state: tabs=%d runtimes=%d/%d", len(app.tabs), len(app.runtimeByID), len(app.runtimeBySessionKey))
	}
	persisted := loadTabsFile()
	if len(persisted.Tabs) != 0 || persisted.ActiveTab != "" {
		t.Fatalf("stale presentation remained persisted: %+v", persisted)
	}
	select {
	case <-app.tabsRestored:
	default:
		t.Fatal("restore completion gate remained open")
	}

	second := NewApp()
	second.ctx = t.Context()
	pinDesktopSessionRoot(t, second)
	t.Cleanup(func() { _ = second.draftStore().Close() })
	finishSavedTabMigration(second)
	second.tabsRestored = make(chan struct{})
	second.restoreOrBuildTabs()
	if len(second.tabs) != 0 || len(second.runtimeByID) != 0 || len(loadTabsFile().Tabs) != 0 {
		t.Fatal("saved-tab repair was not idempotent across restart")
	}
}

func TestDesktopTabsUnknownFieldsSurviveRoundTrip(t *testing.T) {
	var file desktopTabsFile
	raw := []byte(`{
  "tabs": [{"id":"local","scope":"global","workspaceRoot":"","topicId":"topic","futureLocal":{"enabled":true}}],
  "activeTab":"local",
  "remoteTabs":[{"id":"remote","hostId":"host","workspace":"/work","futureRemote":7}],
  "futureTop":{"version":4}
}`)
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["futureTop"]) != `{"version":4}` {
		t.Fatalf("top-level unknown field lost: %s", body)
	}
	var localFields, remoteFields map[string]json.RawMessage
	if err := json.Unmarshal(mustMarshalJSON(t, file.Tabs[0]), &localFields); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mustMarshalJSON(t, file.RemoteTabs[0]), &remoteFields); err != nil {
		t.Fatal(err)
	}
	if string(localFields["futureLocal"]) != `{"enabled":true}` || string(remoteFields["futureRemote"]) != "7" {
		t.Fatalf("entry unknown fields lost: local=%s remote=%s", mustMarshalJSON(t, file.Tabs[0]), mustMarshalJSON(t, file.RemoteTabs[0]))
	}
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestReconcileSavedTabsDropsRetiredDraftOperation(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	finishSavedTabMigration(app)
	root := t.TempDir()
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	draft, _, err := app.draftStore().Open(t.Context(), workspaceID, "project", root, "draft-reconcile", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := app.draftStore().BeginOperation(context.Background(), draftstate.Operation{
		ID: "draft-op-retired", DraftID: draft.ID, WorkspaceID: workspaceID, DraftRevision: draft.Revision,
		SessionID: "draft-session", SubmissionID: "submission", Fingerprint: "fingerprint", RequestJSON: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	file := desktopTabsFile{Tabs: []desktopTabEntry{savedProjectTab("draft", op.SessionID, op.ID, root, workspaceID)}, ActiveTab: "draft"}
	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if !changed || len(got.Tabs) != 0 {
		t.Fatalf("retired draft operation was restored: changed=%v file=%+v", changed, got)
	}
}

func TestReconcileSavedTabsPreservesResidualCanonicalArtifacts(t *testing.T) {
	for _, fixture := range []struct {
		name       string
		legacyPath bool
	}{
		{name: "partial canonical directory"},
		{name: "legacy path on canonical tab", legacyPath: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			app := newSavedTabReconcileTestApp(t)
			finishSavedTabMigration(app)
			entry := desktopTabEntry{ID: "saved", Scope: "global", SessionID: "missing-manifest"}
			artifact := filepath.Join(app.desktopSessions.root, entry.SessionID, "events.jsonl")
			if fixture.legacyPath {
				artifact = filepath.Join(t.TempDir(), "history.jsonl")
				entry.SessionPath = artifact
			}
			if err := os.MkdirAll(filepath.Dir(artifact), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(artifact, []byte("{\"role\":\"user\",\"content\":\"history\"}\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			got, changed := app.reconcileSavedTabs(t.Context(), desktopTabsFile{Tabs: []desktopTabEntry{entry}})
			if changed || len(got.Tabs) != 1 {
				t.Fatalf("durable artifacts were hidden: changed=%v file=%+v", changed, got)
			}
		})
	}
}

func TestPersistReconciledTabsRejectsStaleStartupSnapshot(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	expectedVersion := app.tabsSnapshotVersion()
	app.mu.Lock()
	app.tabs["new"] = &WorkspaceTab{ID: "new", Scope: "global", SessionID: "new-session"}
	app.tabOrder = []string{"new"}
	app.activeTabID = "new"
	dir, entries, activeID, version := app.saveTabsCollectLocked()
	app.mu.Unlock()
	app.saveTabsWrite(dir, entries, activeID, version)

	stale := desktopTabsFile{Tabs: []desktopTabEntry{{ID: "old", Scope: "global", SessionID: "old-session"}}, ActiveTab: "old"}
	if _, err := app.persistReconciledTabsFile(stale, expectedVersion); !errors.Is(err, errTabsSnapshotChanged) {
		t.Fatalf("stale startup write error = %v, want %v", err, errTabsSnapshotChanged)
	}
	if got := loadTabsFile(); got.ActiveTab != "new" {
		t.Fatalf("stale startup snapshot overwrote newer save: %+v", got)
	}
}

func TestReconcileSavedTabsPreservesRecoveryOwnedEmptyConflict(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	canonicalRoot := t.TempDir()
	ref, workspaceID := createLegacyCleanupSession(t, app, canonicalRoot, "recovery-owned-conflict", false)
	if err := app.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().RecordRecovery(t.Context(), workspacestate.RecoveryEntry{
		ID: "recovery", SourceKey: "source", SessionID: ref.SessionID, WorkspaceID: workspaceID,
		Scope: "project", WorkspaceRoot: canonicalRoot, Format: "canonical", Reason: "interrupted", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	workspace := state.Workspaces[workspaceID]
	workspace.Root = t.TempDir()
	state.Workspaces[workspaceID] = workspace
	if err := os.WriteFile(app.workspaceRegistry().Path(), mustMarshalJSON(t, state), 0o600); err != nil {
		t.Fatal(err)
	}
	finishSavedTabMigration(app)
	file := desktopTabsFile{Tabs: []desktopTabEntry{savedProjectTab("recover", ref.SessionID, "", canonicalRoot, workspaceID)}}

	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if changed || len(got.Tabs) != 1 {
		t.Fatalf("recovery-owned conflict was removed: changed=%v file=%+v", changed, got)
	}
	state, err = app.workspaceRegistry().Load(t.Context())
	if err != nil || state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Active {
		t.Fatalf("recovery-owned lifecycle = %q, err=%v", state.SessionStates[ref.SessionID].Lifecycle, err)
	}
}

func TestReconcileSavedTabsPreservesChangedSourceWithInactiveMapping(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(path, []byte("{\"role\":\"user\",\"content\":\"new history\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evidence := savedTabReconcileEvidence{registry: workspacestate.State{
		SourceMappings: map[string]workspacestate.SourceMapping{"old": {Path: path, SessionID: "archived", Fingerprint: "old-fingerprint"}},
		SessionStates:  map[string]workspacestate.SessionState{"archived": {Lifecycle: workspacestate.Archived}},
	}}
	decision := app.classifyLegacySavedTab(desktopTabEntry{ID: "saved", Scope: "global", SessionPath: path}, evidence)
	if decision.outcome != preserveRecovery {
		t.Fatalf("changed legacy source decision = %+v, want preserve recovery", decision)
	}
}

func TestBindLegacyRecoveryOwnerDoesNotCreateReplacement(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	path := filepath.Join(t.TempDir(), "missing.jsonl")
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().RecordRecovery(t.Context(), workspacestate.RecoveryEntry{
		ID: "recovery", SourceKey: "source", Path: path, WorkspaceID: workspaceID,
		Scope: "global", Format: "legacy", Reason: "interrupted", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	ctrl := control.New(control.Options{
		Executor:       agent.New(nil, nil, agent.NewSession("test"), agent.Options{}, event.Discard),
		SessionService: app.desktopSessionService(""), ExclusiveSession: true,
	})
	t.Cleanup(ctrl.Close)
	ref, _, err := app.bindTabCanonicalSession(t.Context(), ctrl, &config.Config{}, "global", "", "", path, "", false)
	if !errors.Is(err, errLegacySourceRecoveryPending) || ref.SessionID != "" {
		t.Fatalf("legacy recovery fallback = ref:%+v err:%v", ref, err)
	}
}

func TestReconcileSavedTabsPreservesWhenMigrationFailed(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	app.desktopMigrationFailed.Store(true)
	finishSavedTabMigration(app)
	file := desktopTabsFile{Tabs: []desktopTabEntry{{ID: "legacy", Scope: "global", SessionPath: filepath.Join(t.TempDir(), "missing.jsonl")}}}
	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if changed || len(got.Tabs) != 1 {
		t.Fatalf("failed migration discarded saved state: changed=%v file=%+v", changed, got)
	}
}

// A 1.38 desktop saves the chat it opened at launch as the active tab; when
// that chat was never sent it has sidecars but no conversation, and the
// upgrade restores a fresh chat instead of a startup error.
func TestReconcileSavedTabsDropsNeverWrittenLegacySession(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra string
		want  savedTabReconcileOutcome
	}{
		{name: "never sent", want: dropStalePresentation},
		{name: "transcript", extra: ".jsonl", want: preserveRecovery},
		{name: "event log", extra: ".events.jsonl", want: preserveRecovery},
		{name: "turn log", extra: ".turns.jsonl", want: preserveRecovery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newSavedTabReconcileTestApp(t)
			stem := filepath.Join(t.TempDir(), "20260926-091504.474467700-fake-model")
			path := stem + ".jsonl"
			if err := os.MkdirAll(stem+".inbox", 0o700); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{
				path + ".meta":            `{"id":"20260926-091504.474467700-fake-model","scope":"global"}`,
				stem + ".goal-state.json": `{"status":"stopped","researchMode":2,"deliveryCheckpoint":{}}`,
				filepath.Join(stem+".inbox", "transaction.lock"): "",
			} {
				if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.extra != "" {
				if err := os.WriteFile(stem+tc.extra, []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			entry := desktopTabEntry{ID: "saved", Scope: "global", SessionPath: path}
			if got := app.classifyLegacySavedTab(entry, savedTabReconcileEvidence{}); got.outcome != tc.want {
				t.Fatalf("decision = %+v, want %s", got, tc.want)
			}
			// Migration may already have mapped the source to a session it archived.
			archived := savedTabReconcileEvidence{registry: workspacestate.State{
				SourceMappings: map[string]workspacestate.SourceMapping{"src": {Path: path, SessionID: "archived"}},
				SessionStates:  map[string]workspacestate.SessionState{"archived": {Lifecycle: workspacestate.Archived}},
			}}
			if got := app.classifyLegacySavedTab(entry, archived); got.outcome != tc.want {
				t.Fatalf("archived mapping decision = %+v, want %s", got, tc.want)
			}
			if _, err := os.Stat(path + ".meta"); err != nil {
				t.Fatalf("classification touched the session files: %v", err)
			}
		})
	}
}
