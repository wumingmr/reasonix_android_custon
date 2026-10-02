package main

import "reasonix/internal/control"

// tabRuntimeSnapshot is a consistent under-a.mu copy of the per-tab fields
// that bound methods and rebuild paths need after releasing the lock.
type tabRuntimeSnapshot struct {
	ctrl                          control.SessionAPI
	sink                          *tabEventSink
	label                         string
	ready                         bool
	readOnly                      bool
	startupErr                    string
	scope                         string
	workspaceRoot                 string
	sessionPath                   string
	sessionID                     string
	sessionGeneration             uint64
	topicID                       string
	topicTitle                    string
	sharedHostKey                 string
	model                         string
	effort                        *string
	tokenMode, qualityFloor, mode string
	goal, toolApprovalMode        string
}

// normalizedTabRuntime is the internal, orthogonal runtime profile restored
// across controller rebuilds. Goal sidecars remain authoritative; legacyGoal is
// only a fallback for a running legacy Goal with no sidecar.
type normalizedTabRuntime struct {
	collaborationMode, toolApprovalMode, tokenMode string
	qualityFloor, legacyGoal                       string
}

// snapshotTabRuntimeLocked copies the racy per-tab fields. Callers must hold
// a.mu (read or write side); controller methods run only after unlocking.
func snapshotTabRuntimeLocked(tab *WorkspaceTab) tabRuntimeSnapshot {
	if tab == nil {
		return tabRuntimeSnapshot{}
	}
	return tabRuntimeSnapshot{
		ctrl: tab.Ctrl, sink: tab.sink, label: tab.Label, ready: tab.Ready,
		readOnly: tab.ReadOnly, startupErr: tab.StartupErr, scope: tab.Scope,
		workspaceRoot: tab.WorkspaceRoot, sessionPath: tab.SessionPath, sessionID: tab.SessionID,
		sessionGeneration: tab.SessionGeneration, topicID: tab.TopicID, topicTitle: tab.TopicTitle,
		sharedHostKey: tab.SharedHostKey, model: tab.model, effort: cloneStringPtr(tab.effort),
		tokenMode: currentTabTokenMode(tab), qualityFloor: tab.qualityFloor, mode: tab.mode,
		goal: tab.goal, toolApprovalMode: tab.toolApprovalMode,
	}
}

func (a *App) tabRuntimeSnapshot(tab *WorkspaceTab) tabRuntimeSnapshot {
	if tab == nil {
		return tabRuntimeSnapshot{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return snapshotTabRuntimeLocked(tab)
}
