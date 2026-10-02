package main

import (
	"errors"
	"fmt"
	"log/slog"
)

type workspaceTabCandidate struct {
	id  string
	tab *WorkspaceTab
}

// The caller holds the runtime-mutation and session-removal barriers. Snapshot
// off App.mu, then verify that neither a running runtime nor a replacement tab
// appeared before durable removal begins.
func (a *App) snapshotWorkspaceTabsForRemoval(dir string) ([]workspaceTabCandidate, error) {
	a.mu.Lock()
	if a.hasRunningWorkspaceRuntimeLocked(dir) {
		a.mu.Unlock()
		return nil, fmt.Errorf("workspace has running sessions; stop them before removing")
	}
	candidates := make([]workspaceTabCandidate, 0)
	for id, tab := range a.tabs {
		if tabInWorkspace(tab, dir) {
			candidates = append(candidates, workspaceTabCandidate{id: id, tab: tab})
		}
	}
	a.mu.Unlock()

	snapshotted := make(map[string]*WorkspaceTab, len(candidates))
	for _, candidate := range candidates {
		snapshotted[candidate.id] = candidate.tab
		if err := a.snapshotTab(candidate.tab); err != nil {
			slog.Warn("desktop: snapshot before removing workspace failed", "tab", candidate.id, "workspace", dir, "err", err)
			return nil, fmt.Errorf("save current session before removing workspace: %w", err)
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hasRunningWorkspaceRuntimeLocked(dir) {
		return nil, fmt.Errorf("workspace has running sessions; stop them before removing")
	}
	for id, tab := range a.tabs {
		if tabInWorkspace(tab, dir) && snapshotted[id] != tab {
			return nil, fmt.Errorf("workspace tabs changed while removing; retry")
		}
	}
	return candidates, nil
}

func (a *App) hasRunningWorkspaceRuntimeLocked(dir string) bool {
	for _, tab := range a.tabs {
		if tabInWorkspace(tab, dir) && tab.hasActiveRuntimeWork() {
			return true
		}
	}
	for _, tab := range a.detachedSessions {
		if tabInWorkspace(tab, dir) && tab.hasActiveRuntimeWork() {
			return true
		}
	}
	return false
}

func (a *App) hideWorkspaceForRemoval(dir string) error {
	workspaceID, err := a.resolveDesktopWorkspaceID(a.bootContext(), "project", dir)
	if err != nil {
		return err
	}
	registry := a.workspaceRegistry()
	state, err := registry.Load(a.bootContext())
	if err != nil {
		return err
	}
	wasVisible := state.Workspaces[workspaceID].Visible
	if wasVisible {
		if err := registry.SetWorkspaceVisible(a.bootContext(), workspaceID, false); err != nil {
			return err
		}
	}
	// Both durable sidebar stores must accept removal before runtime bindings
	// are unlinked. The caller keeps runtime admission frozen throughout.
	if err := removeProject(dir); err != nil {
		if wasVisible {
			if restoreErr := registry.SetWorkspaceVisible(a.bootContext(), workspaceID, true); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("restore workspace visibility: %w", restoreErr))
			}
		}
		return err
	}
	return nil
}

func (a *App) unlinkWorkspaceTabsForRemoval(dir string, candidates []workspaceTabCandidate) (fallback *WorkspaceTab, closeTabs, closeDetached []*WorkspaceTab) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, candidate := range candidates {
		id, tab := candidate.id, candidate.tab
		if tab == nil || a.tabs[id] != tab || !tabInWorkspace(tab, dir) {
			continue
		}
		a.markTabRemovedLocked(tab)
		closeTabs = append(closeTabs, tab)
		delete(a.tabs, id)
		a.removeTabOrderLocked(id)
		if a.activeTabID == id {
			a.activeTabID = ""
		}
	}
	for key, tab := range a.detachedSessions {
		if !tabInWorkspace(tab, dir) {
			continue
		}
		closeDetached = append(closeDetached, tab)
		delete(a.detachedSessions, key)
	}
	if len(a.tabs) == 0 {
		fallback = a.createTabEntry("global", globalTabWorkspaceRoot(), "")
		fallback.TopicTitle = "Global"
		fallback.sink = &tabEventSink{tabID: fallback.ID, app: a, ctx: a.ctx}
		a.tabs[fallback.ID] = fallback
		a.tabOrder = append(a.tabOrder, fallback.ID)
		a.activeTabID = fallback.ID
	} else if a.activeTabID == "" {
		if ordered := a.orderedTabIDsLocked(); len(ordered) > 0 {
			a.activeTabID = ordered[0]
		}
	}
	a.saveTabsLocked()
	return fallback, closeTabs, closeDetached
}
