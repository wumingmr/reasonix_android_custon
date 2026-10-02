package main

import (
	"fmt"
	"os"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/control"
	"reasonix/internal/session"
)

// surfaceForCanonicalSession returns the tab OpenSession installs ref into.
// The active tab wins. Without one (fresh start, remote-only layout, or the
// last visible session was archived), a visible tab that already runs ref is
// activated, then a dormant local tab is reused, and otherwise an empty
// surface is created for ref's workspace and bound by the caller. Opening a
// session must never depend on a replacement blank session having been
// created for it.
func (a *App) surfaceForCanonicalSession(ref session.SessionRef, workspace workspacestate.Workspace) (*WorkspaceTab, control.SessionAPI, bool, error) {
	if tab, ctrl := a.tabAndCtrlByID(""); tab != nil {
		return tab, ctrl, false, nil
	}
	a.mu.Lock()
	if owner := a.liveRuntimeTabMatchingLocked(nil, sessionRoute(ref.SessionID)); owner != nil && a.tabs[owner.ID] == owner && !owner.removed {
		a.activeTabID = owner.ID
		a.saveTabsLocked()
		ctrl := owner.Ctrl
		a.mu.Unlock()
		return owner, ctrl, false, nil
	}
	a.mu.Unlock()

	if tab, ctrl := a.firstResolvableLocalTab(); tab != nil {
		// A remote-only layout keeps one dormant local tab for local work;
		// opening a session there beats creating a second surface.
		a.mu.Lock()
		if a.tabs[tab.ID] == tab && !tab.removed {
			a.activeTabID = tab.ID
			a.saveTabsLocked()
			a.mu.Unlock()
			return tab, ctrl, false, nil
		}
		a.mu.Unlock()
	}

	scope := canonicalWorkspaceScope(workspace)
	workspaceRoot := ""
	if scope == "project" {
		workspaceRoot = normalizeProjectRoot(workspace.Root)
		if workspaceRoot == "" {
			return nil, nil, false, fmt.Errorf("session workspace root is unavailable")
		}
	}
	actualRoot := desktopWorkspaceRoot(scope, workspaceRoot)
	if scope != "project" {
		if err := os.MkdirAll(actualRoot, 0o755); err != nil {
			return nil, nil, false, fmt.Errorf("create global workspace: %w", err)
		}
	}
	releaseAdmission, err := a.beginProjectRuntimeAdmission(scope, actualRoot)
	if err != nil {
		return nil, nil, false, err
	}
	defer releaseAdmission()
	if scope == "project" {
		saveWorkspace(workspaceRoot)
		if err := a.registerProjectRoot(workspaceRoot); err != nil {
			return nil, nil, false, err
		}
	}
	model, toolApprovalMode := desktopNewSessionDefaults(scope, actualRoot)
	tab := &WorkspaceTab{
		Scope:            scope,
		WorkspaceRoot:    actualRoot,
		model:            model,
		mode:             tabModeFromAxes(false, toolApprovalMode == control.ToolApprovalDangerFullAccess),
		toolApprovalMode: toolApprovalMode,
		disabledMCP:      map[string]ServerView{},
	}
	a.mu.Lock()
	if existing := a.activeTabLocked(); existing != nil {
		// Another navigation published a surface meanwhile; install into it.
		ctrl := existing.Ctrl
		a.mu.Unlock()
		return existing, ctrl, false, nil
	}
	tab.ID = a.newUniqueTabIDLocked()
	tab.sink = &tabEventSink{tabID: tab.ID, app: a}
	a.tabs[tab.ID] = tab
	a.tabOrder = append(a.tabOrder, tab.ID)
	a.activeTabID = tab.ID
	a.saveTabsLocked()
	a.mu.Unlock()
	return tab, nil, true, nil
}

// discardUnboundSurface removes a surface created for OpenSession whose
// binding failed, so a failed open leaves no empty tab behind.
func (a *App) discardUnboundSurface(tab *WorkspaceTab) {
	if tab == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tabs[tab.ID] != tab || tab.Ctrl != nil || tab.SessionID != "" {
		return
	}
	a.markTabRemovedLocked(tab)
	delete(a.tabs, tab.ID)
	a.removeTabOrderLocked(tab.ID)
	if a.activeTabID == tab.ID {
		a.activeTabID = ""
		if len(a.tabOrder) > 0 {
			a.activeTabID = a.tabOrder[0]
		}
	}
	a.saveTabsLocked()
}
