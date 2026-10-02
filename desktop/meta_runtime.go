package main

import (
	"os"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	goaldomain "reasonix/internal/goal"
	"reasonix/internal/session"
)

func (a *App) metaForTab(tabID string) Meta {
	for {
		a.mu.RLock()
		tab := a.tabByIDLocked(tabID)
		snap := snapshotTabRuntimeLocked(tab)
		runtimeView := a.sessionRuntimeViewLocked(tab)
		var historical *SessionSourceRef
		headID := ""
		if tab != nil {
			historical, headID = tab.HistoricalSource, tab.SessionHeadID
		}
		a.mu.RUnlock()
		if tab == nil {
			meta := Meta{EventChannel: eventChannel}
			if ref, ok := a.remoteTabRefFor(tabID); ok {
				meta.Remote = &ref
			}
			return meta
		}
		cwd := snap.workspaceRoot
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		extras, refreshExtras := tabMetaExtrasFor(tab, cwd, snap.model)
		if capability, ok := snap.ctrl.(interface{ ImageInputSnapshot() (bool, bool, bool) }); ok {
			if enabled, fallback, available := capability.ImageInputSnapshot(); available {
				extras.imageInputEnabled, extras.visionFallbackEnabled = enabled, fallback
			}
		}
		if refreshExtras {
			a.scheduleTabMetaExtrasRefresh(tab.ID)
		}
		autoApproveTools := snap.ctrl != nil && snap.ctrl.AutoApproveTools()
		goal := snap.currentGoal()
		goalStatus := snap.currentGoalStatus()
		var runtimeStateSnapshot *event.RuntimeStateSnapshot
		var goalView *goaldomain.View
		var canonicalTodos *[]evidence.TodoItem
		if snap.ctrl != nil {
			state := controllerRuntimeState(snap.ctrl)
			if state.SchemaVersion == 1 {
				runtimeStateSnapshot = &state
				goalView = state.Goal
				canonicalTodos = runtimeSnapshotTodos(state)
			} else {
				canonicalTodos = ctrlTodos(snap.ctrl)
			}
		}
		sessionPath := strings.TrimSpace(snap.sessionPath)
		// Read off the lock, like every other controller read in this function.
		if native, ok := snap.ctrl.(*control.Controller); ok && historical == nil {
			path := sessionPath
			if path == "" {
				path = native.SessionPath()
			}
			historical = nativeHistoricalSource(native, path, headID)
		}
		sessionID := strings.TrimSpace(snap.sessionID)
		var sessionRef *session.SessionRef
		if sessionID != "" {
			ref := session.SessionRef{HostID: localDesktopHostID, SessionID: sessionID}
			sessionRef = &ref
		}
		var sessionRevision int64
		var sessionDigest string
		if branchMeta, ok, err := agent.LoadBranchMeta(sessionPath); err == nil && ok {
			sessionRevision = branchMeta.Revision
			sessionDigest = branchMeta.ContentDigest
		}
		meta := Meta{
			Label: snap.label, Ready: runtimeView.Phase == sessionRuntimeReady && snap.ctrl != nil,
			Runtime: runtimeView, StartupErr: snap.startupErr, EventChannel: eventChannel,
			HistoricalSource: historical,
			SessionPath:      sessionPath, SessionID: sessionID, Session: sessionRef,
			SessionRevision: sessionRevision, SessionDigest: sessionDigest,
			SessionGeneration: snap.sessionGeneration, RuntimeStateSnapshot: runtimeStateSnapshot,
			Cwd: cwd, WorkspaceRoot: cwd, WorkspaceName: tabWorkspaceNameForScope(snap.scope, cwd), WorkspacePath: cwd,
			GitBranch: extras.gitBranch, ImageInputEnabled: extras.imageInputEnabled,
			VisionFallbackEnabled: extras.visionFallbackEnabled, AutoApproveTools: autoApproveTools,
			Bypass: autoApproveTools, CollaborationMode: snap.collaborationMode(),
			TokenMode: boot.TokenModeFull, AgentPreset: boot.AgentPresetBalanced,
			ToolApprovalMode: snap.currentToolApprovalMode(), Goal: goal, GoalStatus: goalStatus,
			GoalView: goalView, GoalRuntime: goalRuntimeViewFromController(snap.ctrl), CanonicalTodos: canonicalTodos,
			PinnedFiles: buildPinnedContext(snap.workspaceRoot, tab.GetPinnedFiles()).Infos,
		}
		a.mu.RLock()
		currentTab := a.tabByIDLocked(tabID)
		current := snapshotTabRuntimeLocked(currentTab)
		valid := currentTab == tab && sameSessionAPI(current.ctrl, snap.ctrl) &&
			current.sessionID == snap.sessionID && current.sessionPath == snap.sessionPath &&
			current.sessionGeneration == snap.sessionGeneration
		a.mu.RUnlock()
		if valid {
			return meta
		}
	}
}

func runtimeSnapshotTodos(state event.RuntimeStateSnapshot) *[]evidence.TodoItem {
	todos := make([]evidence.TodoItem, len(state.Todos))
	for i, todo := range state.Todos {
		todos[i] = evidence.TodoItem{Content: todo.Content, Status: todo.Status}
	}
	return &todos
}
