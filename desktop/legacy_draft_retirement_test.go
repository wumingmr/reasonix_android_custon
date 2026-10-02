package main

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
)

func corruptRetiredDraftDatabase(t *testing.T) {
	t.Helper()
	path := config.DesktopDraftStatePath()
	content := []byte("unreadable historical drafts must not block current sessions")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(content) {
			t.Fatalf("historical draft database changed: %q %v", got, err)
		}
	})
}

func TestManualCreationIgnoresRetiredDraftDatabase(t *testing.T) {
	app := newManualSessionTestApp(t)
	corruptRetiredDraftDatabase(t)
	const operationID = "manual-with-retired-drafts"
	if _, err := app.BeginManualSessionCreation(ManualSessionCreationRequest{OperationID: operationID, Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	app.manualCreationTasks.Wait()
	view, err := app.GetManualSessionCreation(operationID)
	if err != nil || view.Phase != "ready" {
		t.Fatalf("formal creation depended on retired drafts: %+v %v", view, err)
	}
}

func TestReconcileSavedTabsIgnoresRetiredDraftDatabase(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	finishSavedTabMigration(app)
	corruptRetiredDraftDatabase(t)
	root := t.TempDir()
	ref, workspaceID := createLegacyCleanupSession(t, app, root, "formal-history", true)
	file := desktopTabsFile{Tabs: []desktopTabEntry{savedProjectTab("formal", ref.SessionID, "draft-op-previous", root, workspaceID)}, ActiveTab: "formal"}
	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if !changed || len(got.Tabs) != 1 || got.Tabs[0].SessionID != ref.SessionID || got.Tabs[0].CreateOperationID != "" || got.Tabs[0].restoreBlocked {
		t.Fatalf("formal session was not restored independently of retired draft: %+v", got)
	}
	if _, err := app.desktopSessionService("").Query().Snapshot(t.Context(), ref); err != nil {
		t.Fatalf("formal history lost: %v", err)
	}
}

func TestReconcileSavedTabsDropsRetiredDraftPendingCreate(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	finishSavedTabMigration(app)
	root := t.TempDir()
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "draft-op-unfinished"
	if err := app.workspaceRegistry().BeginCreate(t.Context(), workspacestate.PendingCreate{OperationID: operationID, WorkspaceID: workspaceID, SessionID: "never-created"}); err != nil {
		t.Fatal(err)
	}
	file := desktopTabsFile{Tabs: []desktopTabEntry{savedProjectTab("retired", "", operationID, root, workspaceID)}, ActiveTab: "retired"}
	got, changed := app.reconcileSavedTabs(t.Context(), file)
	if !changed || len(got.Tabs) != 0 {
		t.Fatalf("unfinished draft became a recovery prompt: %+v", got)
	}
}

func TestRetiredDraftsStayRetiredWhenTabsWriteFails(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	finishSavedTabMigration(app)
	corruptRetiredDraftDatabase(t)
	root := t.TempDir()
	ref, workspaceID := createLegacyCleanupSession(t, app, root, "formal-write-failure", true)
	file := desktopTabsFile{Tabs: []desktopTabEntry{
		savedProjectTab("formal", ref.SessionID, "draft-op-completed", root, workspaceID),
		savedProjectTab("abandoned", "never-created", "draft-op-abandoned", root, workspaceID),
	}, ActiveTab: "abandoned", TabOrder: []string{"abandoned", "formal"}}
	// A directory at the atomic write's temporary path deterministically fails
	// persistence, without relying on OS-specific permission behavior.
	if err := os.MkdirAll(filepath.Join(desktopConfigDir(), tabsFileName+".tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	got, _, current := app.reconcileTabsBeforeRestore(t.Context(), file, app.tabsSnapshotVersion())
	if !current || len(got.Tabs) != 1 || got.Tabs[0].ID != "formal" || got.Tabs[0].SessionID != ref.SessionID || got.Tabs[0].CreateOperationID != "" {
		t.Fatalf("failed tab write revived retired draft ownership: %+v", got)
	}
	if got.ActiveTab != "formal" || len(got.TabOrder) != 1 || got.TabOrder[0] != "formal" {
		t.Fatalf("failed tab write retained abandoned selection: %+v", got)
	}
}

func TestRetiredDraftIdentityRepairWriteFailureStaysBlocked(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	finishSavedTabMigration(app)
	corruptRetiredDraftDatabase(t)
	root := t.TempDir()
	ref, workspaceID := createLegacyCleanupSession(t, app, root, "formal-route-repair", true)
	entry := savedProjectTab("formal", "", "draft-op-completed", root, workspaceID)
	entry.SessionPath = sessionRoute(ref.SessionID)
	file := desktopTabsFile{Tabs: []desktopTabEntry{entry}, ActiveTab: entry.ID}
	if err := os.MkdirAll(filepath.Join(desktopConfigDir(), tabsFileName+".tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	got, _, current := app.reconcileTabsBeforeRestore(t.Context(), file, app.tabsSnapshotVersion())
	if !current || len(got.Tabs) != 1 || got.Tabs[0].CreateOperationID != "" || !got.Tabs[0].restoreBlocked {
		t.Fatalf("unpersisted retirement repair was not blocked: %+v", got)
	}
	if prepareRestoredTabIdentity(&WorkspaceTab{}, got.Tabs[0]) {
		t.Fatal("unpersisted identity repair started a runtime")
	}
}
