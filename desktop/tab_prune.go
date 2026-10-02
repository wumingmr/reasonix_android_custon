package main

import (
	"context"
	"fmt"
	"sync"
)

func (a *App) keepOnlyVisibleTab(tabID string) (TabMeta, error) {
	meta, err := func() (TabMeta, error) {
		defer a.lockRuntimeMutation("prune-visible-tabs")()
		return a.pruneVisibleTabsRuntimeAdmissionHeld(tabID)
	}()
	if err != nil {
		return TabMeta{}, err
	}
	// Visibility and detached/open ownership are runtime state. Snapshot saves
	// already enqueue exact-path catalog updates; switching must not rescan.
	a.emitProjectTreeRuntimeChangedWithLegacy()
	return enrichTabMeta(meta), nil
}

// The caller holds the runtime mutation barrier. The removal lock covers
// snapshots, bindings and teardown; events must be emitted after these locks
// are released so a listener cannot re-enter the removal path.
func (a *App) pruneVisibleTabsRuntimeAdmissionHeld(tabID string) (TabMeta, error) {
	type pruneCandidate struct {
		id  string
		tab *WorkspaceTab
	}
	a.sessionRemovalMu.Lock()
	defer a.sessionRemovalMu.Unlock()

	a.mu.Lock()
	active := a.tabs[tabID]
	if active == nil {
		a.mu.Unlock()
		return TabMeta{}, fmt.Errorf("tab %q not found", tabID)
	}
	candidates := make([]pruneCandidate, 0, len(a.tabs)-1)
	for id, tab := range a.tabs {
		if id == tabID {
			continue
		}
		candidates = append(candidates, pruneCandidate{id: id, tab: tab})
	}
	a.mu.Unlock()

	// Keep bindings visible while saving. Snapshot may invoke recovery
	// callbacks that need App.mu, so it must run outside that lock.
	snapshotted := make(map[string]*WorkspaceTab, len(candidates))
	for _, candidate := range candidates {
		id, tab := candidate.id, candidate.tab
		snapshotted[id] = tab
		if err := a.persistHiddenTabBeforePrune(id, tab); err != nil {
			return TabMeta{}, err
		}
	}

	a.mu.Lock()
	active = a.tabs[tabID]
	if active == nil {
		a.mu.Unlock()
		return TabMeta{}, fmt.Errorf("tab %q not found", tabID)
	}
	for id, tab := range a.tabs {
		if id != tabID && snapshotted[id] != tab {
			a.mu.Unlock()
			return TabMeta{}, fmt.Errorf("visible tabs changed while switching; retry")
		}
	}
	a.activeTabID = tabID
	removed := make([]*WorkspaceTab, 0, len(candidates))
	for _, candidate := range candidates {
		id, tab := candidate.id, candidate.tab
		if tab == nil || a.tabs[id] != tab {
			continue
		}
		if tab.Ctrl == nil || !tab.hasActiveRuntimeWork() {
			a.markTabRemovedLocked(tab)
		}
		removed = append(removed, tab)
		delete(a.tabs, id)
		a.removeTabOrderLocked(id)
	}
	a.tabOrder = []string{tabID}
	// A local topic replaces the remote visible surface too. Leaving its
	// registry entry active makes ListTabs hide the newly selected local tab.
	a.remoteTabMu.Lock()
	remoteCancels := make([]context.CancelFunc, 0, len(a.remoteTabs))
	for id, remote := range a.remoteTabs {
		if remote.cancel != nil {
			remoteCancels = append(remoteCancels, remote.cancel)
		}
		delete(a.remoteTabs, id)
		a.forgetRemoteBrowserExecutor(id)
	}
	a.remoteTabLayout = remoteTabLayoutState{}
	a.remoteTabMu.Unlock()
	a.saveTabsLocked()
	meta := a.tabMeta(active, true)
	a.mu.Unlock()

	for _, cancel := range remoteCancels {
		cancel()
	}
	for _, tab := range removed {
		a.removeVisibleTabRuntimeAdmissionHeld(tab)
	}
	return meta, nil
}

// Async cleanup must not hold singleSurfaceMu while waiting for runtime work:
// that would prevent later clicks from publishing their readable identities.
// Existing synchronous callers lock the surface first, so never block on that
// lock while holding the runtime barrier. Release and wait outside both locks
// if a synchronous caller owns the surface. On return the caller owns both;
// it must release singleSurfaceMu and may release runtime admission early.
func (a *App) lockTopicActivationPrune() func() {
	for {
		unlockRuntime := a.lockRuntimeMutation("prune-visible-tabs")
		if a.singleSurfaceMu.TryLock() {
			return sync.OnceFunc(unlockRuntime)
		}
		unlockRuntime()
		a.singleSurfaceMu.Lock()
		a.singleSurfaceMu.Unlock() //nolint:staticcheck // SA2001: wait for the competing surface owner without retaining the runtime barrier.
	}
}
