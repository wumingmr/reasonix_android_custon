package main

import (
	"fmt"

	"reasonix/internal/config"
	"reasonix/internal/control"
)

// NewSession snapshots the current conversation and rotates to a fresh one.
func (a *App) NewSession() error {
	return a.NewSessionForTab("")
}

// NewSessionForTab snapshots and rotates the requested tab regardless of which
// tab becomes active while the Wails call is in flight.
func (a *App) NewSessionForTab(tabID string) error {
	tab, ctrl := a.tabAndCtrlByID(tabID)
	if a.tabIsReadOnly(tab) {
		return readOnlyChannelErr()
	}
	if ctrl == nil {
		return a.workspaceNotReadyErr(tab)
	}
	if err := a.ensureTabControllerWorkspace(tab); err != nil {
		return err
	}
	ctrl = a.controllerForTab(tab)
	if ctrl == nil {
		return a.workspaceNotReadyErr(tab)
	}
	// Serialize the session rotation with rebuilds, foreground admission, and
	// Pin/Unpin. The default-model rebuild starts only after these locks release.
	unlockRuntime := a.lockRuntimeMutation("new session")
	tab.turnStartMu.Lock()
	released := false
	releaseAdmission := func() {
		if released {
			return
		}
		released = true
		tab.turnStartMu.Unlock()
		unlockRuntime()
	}
	defer releaseAdmission()
	ctrl = a.controllerForTab(tab)
	if ctrl == nil {
		return a.workspaceNotReadyErr(tab)
	}
	// Tab is already blank — skip rotation, but still apply the configured
	// default. A reused empty tab otherwise keeps the previous session's
	// provider after a default-model change (#9080).
	if !controllerHasActiveRuntimeWork(ctrl) && !messagesHaveConversationContent(ctrl.History()) {
		if err := clearBlankSessionPinnedContext(tab, ctrl); err != nil {
			return err
		}
		a.persistTabSessionPath(tab, ctrl.SessionPath())
		a.mu.RLock()
		reused := tab.SessionID
		a.mu.RUnlock()
		if reused != "" {
			// The blank session is handed out as new, so it keeps no earlier
			// choice and no snapshot read before this point can make one.
			a.sessionPresets.forget(reused)
			a.adoptSessionPreset(tab, ctrl, reused)
			if invalidator, ok := ctrl.(permissionSnapshotInvalidator); ok {
				invalidator.InvalidatePermissionSnapshots()
			}
		}
		releaseAdmission()
		if err := a.ensureReusableBlankIdentity(tab); err != nil {
			return err
		}
		return a.applyNewSessionDefaultModel(tab)
	}

	if err := ctrl.NewSession(); err != nil {
		a.syncTabSessionIdentity(tab, ctrl)
		return err
	}
	a.syncTabSessionIdentity(tab, ctrl)
	if path := ctrl.SessionPath(); path != "" {
		if err := savePinnedContextState(path, []string{}); err != nil {
			return fmt.Errorf("initialize empty pinned context for new session: %w", err)
		}
	}
	tab.setPinnedFiles(nil)
	// The rotated session starts with zero spend: without this reset the tab
	// telemetry keeps the previous session's totals and the status bar 会话费用
	// silently turns into an all-sessions running total (#5850).
	tab.resetTelemetry(tab.currentSessionIdentity())
	// Mirror the controller: NewSession cleared the active goal, and the tab's
	// persisted copy must follow — otherwise the next rebuild/restart would
	// re-seed the old goal into the fresh session via SetGoal(tab.goal).
	a.clearTabGoal(tab)
	if err := a.assignFreshSessionTopic(tab); err != nil {
		return fmt.Errorf("session created; persist topic presentation: %w", err)
	}
	a.persistTabSessionPath(tab, ctrl.SessionPath())
	a.invalidatePromptHistoryCache()
	a.emitProjectTreeChangedForSessionDirs(ctrl.SessionDir())
	releaseAdmission()
	return a.applyNewSessionDefaultModel(tab)
}

func (a *App) syncTabSessionIdentity(tab *WorkspaceTab, ctrl control.SessionAPI) {
	if a == nil || tab == nil || ctrl == nil {
		return
	}
	identity, ok := ctrl.(control.IdentityLifecycle)
	if !ok || !identity.UsesExclusiveSession() {
		return
	}
	ref, bound := identity.SessionRef()
	if !bound {
		return
	}
	rebound := false
	a.mu.Lock()
	if current := a.tabs[tab.ID]; current == tab {
		if tab.SessionID == "" && tab.SessionPath != "" && identity.SessionService() != a.desktopSessionService("") {
			// A historical directory has an identity only within its original
			// store. Keep the path locator until a new canonical session is bound.
			a.mu.Unlock()
			return
		}
		if tab.SessionID != ref.SessionID {
			tab.SessionGeneration++
			if tab.sink != nil {
				tab.sink.setSessionGeneration(tab.SessionGeneration)
			}
			rebound = true
		}
		tab.SessionID = ref.SessionID
		tab.SessionHeadID = ""
		tab.SessionPath = ""
		a.bindSessionRuntimeKeyLocked(tab, tab.currentSessionIdentity())
	}
	a.mu.Unlock()
	if rebound {
		a.adoptSessionPreset(tab, ctrl, ref.SessionID)
	}
	a.saveTabsFromRemote()
}

type permissionSnapshotInvalidator interface {
	InvalidatePermissionSnapshots()
}

// adoptSessionPreset gives a tab that now holds sessionID that session's own
// recorded preset, or the new-session default; the preset the surface held for
// its previous session never carries over.
func (a *App) adoptSessionPreset(tab *WorkspaceTab, ctrl control.SessionAPI, sessionID string) {
	preset := a.sessionPresets.restore(sessionID, newSessionPreset(config.LoadForEdit(config.UserConfigPath())))
	a.mu.Lock()
	if a.tabs[tab.ID] != tab || tab.SessionID != sessionID {
		a.mu.Unlock()
		return
	}
	tab.toolApprovalMode = preset
	tab.mode = tabModeFromAxes(tabModeHasPlan(tab.mode), preset == control.ToolApprovalDangerFullAccess)
	a.mu.Unlock()
	applyTabToolApprovalModeToController(ctrl, preset)
}

func clearBlankSessionPinnedContext(tab *WorkspaceTab, ctrl control.SessionAPI) error {
	oldFiles := tab.GetPinnedFiles()
	path := ctrl.SessionPath()
	if path != "" {
		if err := savePinnedContextState(path, []string{}); err != nil {
			return err
		}
	}
	if len(oldFiles) > 0 {
		tab.setPinnedFiles(nil)
	}
	return nil
}

func installClearedTabRuntime(tab *WorkspaceTab, ctrl control.SessionAPI, sink *tabEventSink, path string) {
	tab.Ctrl = ctrl
	tab.sink = sink
	tab.SessionPath = path
	tab.Label = ctrl.Label()
	tab.Ready = true
	tab.setPinnedFiles(nil)
}
