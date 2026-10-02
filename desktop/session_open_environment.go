package main

import (
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/plugin"
)

type sessionOpenEnvironment struct {
	snapshot         tabRuntimeSnapshot
	config           *config.Config
	host             *plugin.Host
	oldHost          string
	acquiredHost     bool
	workspaceChanged bool
	preserveSource   bool
	releaseAdmission func()
}

func (a *App) prepareSessionOpenEnvironment(tab *WorkspaceTab, workspace workspacestate.Workspace) (*sessionOpenEnvironment, error) {
	snap := a.tabRuntimeSnapshot(tab)
	prepared := &sessionOpenEnvironment{snapshot: snap, oldHost: snap.sharedHostKey, workspaceChanged: canonicalWorkspaceChanged(snap, workspace)}
	prepared.preserveSource = controllerHasActiveRuntimeWork(snap.ctrl)
	prepared.snapshot.sink = &tabEventSink{tabID: tab.ID, app: a}
	prepared.snapshot.scope, prepared.snapshot.workspaceRoot = canonicalWorkspaceScope(workspace), workspace.Root
	release, err := a.beginProjectRuntimeAdmission(prepared.snapshot.scope, workspace.Root)
	if err != nil {
		return nil, err
	}
	prepared.releaseAdmission = release
	prepared.config, err = config.LoadForRoot(workspace.Root)
	if err != nil {
		release()
		return nil, err
	}
	prepared.host = a.lookupSharedHost(snap.sharedHostKey)
	if prepared.workspaceChanged || snap.ctrl == nil || prepared.preserveSource {
		prepared.snapshot.sharedHostKey = workspace.Root
		prepared.host = a.acquireSharedHost(workspace.Root)
		prepared.acquiredHost = true
	}
	return prepared, nil
}

func (a *App) finishSessionOpenEnvironment(prepared *sessionOpenEnvironment, committed bool) {
	prepared.releaseAdmission()
	if !prepared.acquiredHost {
		return
	}
	if !committed {
		a.releaseSharedHost(prepared.snapshot.sharedHostKey)
	} else if prepared.oldHost != "" && !prepared.preserveSource {
		a.releaseSharedHost(prepared.oldHost)
	}
}

// preset is the target session's own restored value; the source surface's
// preset never crosses into another session.
func prepareCanonicalControllerRuntime(candidate control.SessionAPI, snap tabRuntimeSnapshot, preset string) normalizedTabRuntime {
	candidate.EnableInteractiveApproval()
	runtime := snap.normalizedRuntime()
	runtime.toolApprovalMode = normalizeToolApprovalMode(preset)
	applyTabToolApprovalModeToController(candidate, runtime.toolApprovalMode)
	applyTabQualityFloorToController(candidate, runtime.qualityFloor)
	runtime.collaborationMode, runtime.legacyGoal = "normal", ""
	if candidate.PlanMode() {
		runtime.collaborationMode = "plan"
	}
	if candidate.GoalStatus() == control.GoalStatusRunning && strings.TrimSpace(candidate.Goal()) != "" && !candidate.PlanMode() {
		runtime.collaborationMode, runtime.legacyGoal = "goal", strings.TrimSpace(candidate.Goal())
	}
	return runtime
}
