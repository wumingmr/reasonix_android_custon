package main

import (
	"testing"

	"reasonix/internal/session"
)

// removeOpenFixtureTab leaves the app with no visible surface, the state after
// the last session was archived or before the first one was ever opened.
func removeOpenFixtureTab(t *testing.T, app *App, tab *WorkspaceTab) {
	t.Helper()
	app.mu.Lock()
	app.markTabRemovedLocked(tab)
	app.releaseSessionRuntimeLocked(tab)
	delete(app.tabs, tab.ID)
	app.removeTabOrderLocked(tab.ID)
	app.activeTabID = ""
	app.mu.Unlock()
	if tab.Ctrl != nil {
		tab.Ctrl.Close()
		tab.Ctrl = nil
	}
}

// Opening a session from the sidebar or from an accepted draft must work when
// no surface exists; it used to fail with "workspace is not ready" and relied
// on a replacement blank session being present.
func TestOpenSessionWithoutSurfaceCreatesOneForTheSession(t *testing.T) {
	app, tab, target, rootB, workspaceB := canonicalWorkspaceOpenFixture(t)
	removeOpenFixtureTab(t, app, tab)

	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatalf("OpenSession without a surface: %v", err)
	}
	app.mu.RLock()
	active := app.tabs[app.activeTabID]
	count := len(app.tabs)
	app.mu.RUnlock()
	if active == nil || count != 1 {
		t.Fatalf("expected exactly one active surface, got active=%v tabs=%d", active != nil, count)
	}
	t.Cleanup(func() {
		if live := app.controllerForTab(active); live != nil {
			live.Close()
		}
	})
	if active.SessionID != target.Ref().SessionID || !active.Ready || active.StartupErr != "" || !sameDesktopPath(active.WorkspaceRoot, rootB) || active.SessionWorkspace.ID != workspaceB {
		t.Fatalf("surface bound wrong session: %+v", persistedDesktopTabEntry(active))
	}
	persisted := loadTabsFile()
	if len(persisted.Tabs) != 1 || persisted.Tabs[0].SessionID != target.Ref().SessionID || persisted.ActiveTab != active.ID {
		t.Fatalf("surface not persisted as active: %+v", persisted)
	}
}

// A session that already runs in a visible but inactive tab (the tab a draft
// submission created in the background) is activated in place instead of being
// rebuilt or rejected.
func TestOpenSessionActivatesVisibleTabAlreadyRunningTheSession(t *testing.T) {
	app, tab, _, _, _ := canonicalWorkspaceOpenFixture(t)
	ctrl := tab.Ctrl
	app.mu.Lock()
	app.activeTabID = ""
	app.mu.Unlock()

	if _, err := app.OpenSession(session.SessionRef{HostID: localDesktopHostID, SessionID: tab.SessionID}); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	app.mu.RLock()
	activeID, count := app.activeTabID, len(app.tabs)
	app.mu.RUnlock()
	if activeID != tab.ID || count != 1 || tab.Ctrl != ctrl {
		t.Fatalf("owner tab not activated in place: active=%q tabs=%d sameCtrl=%v", activeID, count, tab.Ctrl == ctrl)
	}
}

// A surface created for an open that then fails must not linger as an empty
// tab; that would be the replacement blank session in another form.
func TestDiscardUnboundSurfaceRemovesOnlyTheEmptyTab(t *testing.T) {
	app, tab, target, _, _ := canonicalWorkspaceOpenFixture(t)
	removeOpenFixtureTab(t, app, tab)
	workspace, err := app.canonicalSessionWorkspace(t.Context(), target.Ref())
	if err != nil {
		t.Fatal(err)
	}
	created, ctrl, isNew, err := app.surfaceForCanonicalSession(target.Ref(), workspace)
	if err != nil || ctrl != nil || !isNew {
		t.Fatalf("surfaceForCanonicalSession = %+v, %v, new=%v, %v", created, ctrl, isNew, err)
	}
	app.mu.RLock()
	activeID := app.activeTabID
	app.mu.RUnlock()
	if activeID != created.ID {
		t.Fatalf("created surface is not active: %q", activeID)
	}
	app.discardUnboundSurface(created)
	app.mu.RLock()
	defer app.mu.RUnlock()
	if len(app.tabs) != 0 || len(app.tabOrder) != 0 || app.activeTabID != "" {
		t.Fatalf("unbound surface lingered: tabs=%d order=%v active=%q", len(app.tabs), app.tabOrder, app.activeTabID)
	}
}
