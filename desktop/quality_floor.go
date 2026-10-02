package main

import (
	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/worktree"
)

// Retired quality-floor compatibility. Old callers and persisted values remain
// readable, while every live session uses standard execution.

// SetQualityFloor validates a legacy value for the active tab.
func (a *App) SetQualityFloor(floor string) error {
	return a.SetQualityFloorForTab("", floor)
}

// SetQualityFloorForTab validates a legacy value and target tab without
// changing runtime state, rebuilding a controller, or starting a turn.
func (a *App) SetQualityFloorForTab(tabID, floor string) error {
	_, err := control.NormalizeQualityFloor(floor)
	if err != nil {
		return err
	}
	tab := a.tabByID(tabID)
	if tab == nil {
		return a.workspaceNotReadyErr(nil)
	}
	return nil
}

func (a *App) validateRemoteQualityFloor(tabID, floor string) error {
	if _, err := control.NormalizeQualityFloor(floor); err != nil {
		return err
	}
	_, _, _, err := a.remoteTabCommandTarget(tabID)
	return err
}

// derivedFloor retains the wire shape while keeping worktree isolation
// independent from the retired quality setting.
type derivedFloor struct {
	floor    string
	inferred bool
	isolated bool
}

// derivedQualityFloor always reports standard and separately reports whether
// the tab uses a managed worktree.
func derivedQualityFloor(tab *WorkspaceTab) derivedFloor {
	if tab == nil {
		return derivedFloor{floor: control.QualityFloorStandard}
	}
	isolated := worktree.IsManagedPath(tab.WorkspaceRoot, config.DeliveryWorktreeDir())
	return derivedFloor{floor: control.QualityFloorStandard, isolated: isolated}
}

// tabQualityFloor returns the only value written by new clients. Its arguments
// remain for compatibility with the session-creation call sites.
func tabQualityFloor(string, string) string {
	return control.QualityFloorStandard
}

// applyTabQualityFloorToController validates legacy input, then pins the live
// controller to the standard compatibility value.
func applyTabQualityFloorToController(ctrl control.SessionAPI, floor string) {
	if ctrl == nil {
		return
	}
	if _, err := control.NormalizeQualityFloor(floor); err != nil {
		return
	}
	_ = ctrl.SetQualityFloor(control.QualityFloorStandard)
}

// currentTabTokenMode returns the fixed dual-write compatibility label.
func currentTabTokenMode(tab *WorkspaceTab) string {
	return tokenModeForFloor(derivedQualityFloor(tab).floor)
}

// currentTabAgentPreset returns the role label derived from the quality floor.
func currentTabAgentPreset(tab *WorkspaceTab) string {
	return agentPresetForFloor(derivedQualityFloor(tab).floor)
}

// tokenModeForFloor and agentPresetForFloor map an already-derived floor onto
// the compat labels, so callers holding a derivedFloor skip the path math.
func tokenModeForFloor(_ string) string {
	return boot.TokenModeFull
}

func agentPresetForFloor(_ string) string {
	return boot.AgentPresetStandard
}

// qualityFloorSafe reads the recorded floor; nil-safe for lookups.
func (t *WorkspaceTab) qualityFloorSafe() string {
	if t == nil {
		return ""
	}
	return t.qualityFloor
}
