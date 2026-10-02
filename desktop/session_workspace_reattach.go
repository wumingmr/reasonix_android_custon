package main

import (
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/control"
	"reasonix/internal/session"
)

// runtimeRebuildMu and the destination turnStartMu are held. Reuse the exact
// live owner before asking the writer registry for a competing controller.
func (a *App) reattachCanonicalSessionRuntime(tab *WorkspaceTab, current control.SessionAPI, ref session.SessionRef, workspace workspacestate.Workspace, navigation uint64) (control.SessionAPI, error) {
	preserveSource := controllerHasActiveRuntimeWork(current)
	a.mu.Lock()
	if navigation != 0 && a.desktopSessions.navigationSeq.Load() != navigation {
		a.mu.Unlock()
		return nil, errSessionNavigationSuperseded
	}
	source := a.liveRuntimeTabMatchingLocked(tab, sessionRoute(ref.SessionID))
	if source == nil || source == tab || source.Ctrl == nil || !source.Ready {
		a.mu.Unlock()
		return nil, nil
	}
	if source.SessionWorkspace.ID != workspace.ID || canonicalWorkspaceScope(workspace) != source.Scope {
		a.mu.Unlock()
		return nil, errSessionWorkspaceConflict
	}
	if a.tabs[tab.ID] != tab || tab.Ctrl != current || tab.removed {
		a.mu.Unlock()
		return nil, errSessionNavigationSuperseded
	}
	oldHost := tab.SharedHostKey
	oldSink := tab.sink
	var terminals []*terminalSession
	if a.terminals != nil {
		terminals = a.terminals.detachForTab(tab.ID)
	}
	if preserveSource {
		if !a.detachRuntimeForReplacementLocked(tab) {
			a.mu.Unlock()
			return nil, errSessionNavigationSuperseded
		}
	} else {
		a.releaseSessionRuntimeLocked(tab)
	}
	a.unregisterDetachedRuntimeLocked(source)
	if a.tabs[source.ID] == source {
		delete(a.tabs, source.ID)
		a.removeTabOrderLocked(source.ID)
	}
	if a.activeTabID == source.ID {
		a.activeTabID = tab.ID
	}
	applyCanonicalWorkspaceLocked(tab, workspace, true)
	applyRuntimeTab(tab, source, sessionRoute(ref.SessionID), a.ctx, a)
	a.supersedeTabBuildLocked(tab)
	a.saveTabsLocked()
	adopted, sink := tab.Ctrl, tab.sink
	epoch := a.advanceSessionRuntimeEpochLocked(tab)
	a.mu.Unlock()
	if !preserveSource {
		fenceCanonicalNavigationSink(oldSink)
		retireReplacedController(current, adopted)
		if oldHost != "" {
			a.releaseSharedHost(oldHost)
		}
	}
	a.finishCanonicalWorkspaceMove(tab.ID, terminals)
	a.replayPendingPromptsAfterRuntimeAttach(tab.ID, sink, adopted, epoch)
	return adopted, nil
}

func (a *App) finishCanonicalWorkspaceMove(tabID string, terminals []*terminalSession) {
	if a.workspaceHub != nil {
		a.workspaceHub.reconcileRoots()
	}
	if a.terminals != nil {
		a.terminals.reopenForTab(tabID)
		a.terminals.closeSessions(terminals)
	}
}
