package cli

import "reasonix/internal/control"

// handleModeShortcut routes every permission/collaboration shortcut through one
// owner. Terminals may emit Shift+Tab as either "shift+tab" or CSI-Z "backtab"
// (#6660); Ctrl+Y retains the user-facing YOLO toggle.
func (m *chatTUI) handleModeShortcut(key string) bool {
	switch {
	case modeToggleKey(key):
		m.cycleMode()
		return true
	case key == "ctrl+y":
		m.toggleYoloMode()
		return true
	default:
		return false
	}
}

// modeToggleKey reports whether s is a recognized Shift+Tab encoding for the
// permission/plan mode cycle.
func modeToggleKey(s string) bool {
	switch s {
	case "shift+tab", "backtab":
		return true
	default:
		return false
	}
}

// cycleMode handles the Shift+Tab gesture using the four user-facing postures:
// read-only → workspace-write → YOLO (danger-full-access) → Plan →
// read-only. Goal is not part of the cycle and is cleared when Plan is entered.
func (m *chatTUI) cycleMode() {
	if m.ctrl == nil {
		return
	}
	switch {
	case m.planMode:
		m.planMode = false
		m.ctrl.SetToolApprovalMode(control.ToolApprovalReadOnly)
		m.yoloRestoreToolApprovalMode = ""
	case m.ctrl.ToolApprovalMode() == control.ToolApprovalDontAsk:
		m.ctrl.SetToolApprovalMode(control.ToolApprovalReadOnly)
		m.yoloRestoreToolApprovalMode = ""
	case m.ctrl.ToolApprovalMode() == control.ToolApprovalReadOnly:
		m.ctrl.SetToolApprovalMode(control.ToolApprovalWorkspaceWrite)
		m.yoloRestoreToolApprovalMode = ""
	case m.ctrl.ToolApprovalMode() == control.ToolApprovalWorkspaceWrite:
		m.yoloRestoreToolApprovalMode = control.ToolApprovalWorkspaceWrite
		m.ctrl.SetToolApprovalMode(control.ToolApprovalDangerFullAccess)
	case m.ctrl.ToolApprovalMode() == control.ToolApprovalDangerFullAccess:
		m.planMode = true
		m.ctrl.SetToolApprovalMode(control.ToolApprovalReadOnly)
		m.yoloRestoreToolApprovalMode = ""
		m.ctrl.ClearGoal()
	}
	m.ctrl.SetPlanMode(m.planMode)
}

// toggleYoloMode keeps the familiar Ctrl+Y/YOLO interaction label while the
// permission owner receives only the canonical danger-full-access preset. A
// second press restores the safe preset that was active before the toggle.
func (m *chatTUI) toggleYoloMode() {
	if m.ctrl == nil {
		return
	}
	if m.ctrl.ToolApprovalMode() == control.ToolApprovalDangerFullAccess {
		restore := m.yoloRestoreToolApprovalMode
		if restore != control.ToolApprovalWorkspaceWrite {
			restore = control.ToolApprovalReadOnly
		}
		m.ctrl.SetToolApprovalMode(restore)
		m.yoloRestoreToolApprovalMode = ""
		return
	}
	restore := m.ctrl.ToolApprovalMode()
	if restore != control.ToolApprovalWorkspaceWrite {
		restore = control.ToolApprovalReadOnly
	}
	m.yoloRestoreToolApprovalMode = restore
	m.ctrl.SetToolApprovalMode(control.ToolApprovalDangerFullAccess)
}
