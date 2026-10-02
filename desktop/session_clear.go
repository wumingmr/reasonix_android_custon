package main

import (
	"fmt"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/session"
)

// SessionClearResult is the post-clear session identity the frontend must apply
// atomically so hydrate/mode-switch cannot re-bind to the destroyed transcript.
type SessionClearResult struct {
	SessionPath       string              `json:"sessionPath"`
	SessionID         string              `json:"sessionId,omitempty"`
	Session           *session.SessionRef `json:"session,omitempty"`
	SessionRevision   int64               `json:"sessionRevision,omitempty"`
	SessionDigest     string              `json:"sessionDigest,omitempty"`
	SessionGeneration uint64              `json:"sessionGeneration"`
}

func initClearedPins(path string, newCtrl, oldCtrl control.SessionAPI, tab *WorkspaceTab) error {
	if err := savePinnedContextState(path, []string{}); err != nil {
		newCtrl.Close()
		tab.releaseSessionLease()
		oldCtrl.CloseAfterDestroy()
		return fmt.Errorf("initialize empty pinned context for cleared session: %w", err)
	}
	return nil
}

func setFreshControllerPath(ctrl control.SessionAPI, path string) {
	if fresh, ok := ctrl.(interface{ SetFreshSessionPath(string) }); ok {
		fresh.SetFreshSessionPath(path)
	} else {
		ctrl.SetSessionPath(path)
	}
}

// ClearSession discards the current conversation and rotates to a fresh unsaved one.
func (a *App) ClearSession() (SessionClearResult, error) {
	return a.ClearSessionForTab("")
}

// ClearSessionForTab clears the requested tab regardless of later focus changes.
// On success it returns the replacement session identity (path/revision/digest
// and a tab-local generation) so the frontend can retire the old transcript
// without waiting for a later MetaForTab round trip.
func (a *App) ClearSessionForTab(tabID string) (SessionClearResult, error) {
	tab, ctrl := a.tabAndCtrlByID(tabID)
	if a.tabIsReadOnly(tab) {
		return SessionClearResult{}, readOnlyChannelErr()
	}
	if ctrl == nil {
		return SessionClearResult{}, a.workspaceNotReadyErr(tab)
	}
	if err := a.ensureTabControllerWorkspace(tab); err != nil {
		return SessionClearResult{}, err
	}
	ctrl = a.controllerForTab(tab)
	if ctrl == nil {
		return SessionClearResult{}, a.workspaceNotReadyErr(tab)
	}
	if controllerHasActiveRuntimeWork(ctrl) {
		return a.clearActiveSessionRuntime(tab, ctrl)
	}
	unlockRuntime := a.lockRuntimeMutation("clear session")
	defer unlockRuntime()
	tab.turnStartMu.Lock()
	defer tab.turnStartMu.Unlock()
	ctrl = a.controllerForTab(tab)
	if ctrl == nil {
		return SessionClearResult{}, a.workspaceNotReadyErr(tab)
	}
	if controllerHasActiveRuntimeWork(ctrl) {
		return SessionClearResult{}, errTopicHasActiveWork
	}
	if err := ctrl.ClearSession(); err != nil {
		a.syncTabSessionIdentity(tab, ctrl)
		return SessionClearResult{}, err
	}
	a.syncTabSessionIdentity(tab, ctrl)
	if path := ctrl.SessionPath(); path != "" {
		if err := savePinnedContextState(path, []string{}); err != nil {
			return SessionClearResult{}, fmt.Errorf("initialize empty pinned context for cleared session: %w", err)
		}
	}
	tab.setPinnedFiles(nil)
	if err := a.ensureTabSessionLeaseForRebuild(tab, ctrl.SessionPath(), ""); err != nil {
		// Wails bridge return: a raw lease error would carry the session path
		// and holder id across to the frontend.
		return SessionClearResult{}, userFacingSessionLeaseError("", err)
	}
	tab.resetTelemetry(ctrl.SessionPath())
	// Mirror the controller: ClearSession cleared the active goal.
	a.clearTabGoal(tab)
	a.persistTabSessionPath(tab, ctrl.SessionPath())
	a.invalidatePromptHistoryCache()
	return a.bumpAndSnapshotSessionClear(tab), nil
}

func (a *App) bumpAndSnapshotSessionClear(tab *WorkspaceTab) SessionClearResult {
	if tab == nil {
		return SessionClearResult{}
	}
	a.mu.Lock()
	tab.SessionGeneration++
	gen := tab.SessionGeneration
	if tab.sink != nil {
		tab.sink.setSessionGeneration(gen)
	}
	path := tab.currentSessionPath()
	sessionID := tab.SessionID
	if path == "" && tab.Ctrl != nil {
		path = tab.Ctrl.SessionPath()
	}
	a.mu.Unlock()
	var sessionRef *session.SessionRef
	if sessionID != "" {
		ref := session.SessionRef{HostID: localDesktopHostID, SessionID: sessionID}
		sessionRef = &ref
	}
	var revision int64
	var digest string
	if meta, ok, err := agent.LoadBranchMeta(path); err == nil && ok {
		revision = meta.Revision
		digest = meta.ContentDigest
	}
	return SessionClearResult{
		SessionPath: path, SessionID: sessionID, Session: sessionRef,
		SessionRevision: revision, SessionDigest: digest, SessionGeneration: gen,
	}
}

// clearTabGoal drops the tab's persisted goal copy so rebuilds and restarts
// cannot re-seed a goal the controller has already cleared on session rotation.
func (a *App) clearTabGoal(tab *WorkspaceTab) {
	if tab == nil {
		return
	}
	a.mu.Lock()
	tab.goal = ""
	if current := a.tabs[tab.ID]; current == tab {
		a.saveTabsLocked()
	}
	a.mu.Unlock()
}
