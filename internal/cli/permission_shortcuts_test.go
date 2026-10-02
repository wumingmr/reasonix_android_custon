package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
)

func TestShiftTabCyclesReadWorkspaceYoloPlan(t *testing.T) {
	m := newTestChatTUI()
	m.ctrl = newOwnedTestController(t, control.Options{})
	m.ctrl.SetGoal("leave goal before plan")
	key := tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	want := []struct {
		mode  string
		plan  bool
		label string
	}{{control.ToolApprovalWorkspaceWrite, false, "Goal+Workspace"}, {control.ToolApprovalDangerFullAccess, false, "Goal+YOLO"}, {control.ToolApprovalReadOnly, true, "Plan"}, {control.ToolApprovalReadOnly, false, "Read only"}}
	for i, step := range want {
		out, _ := m.Update(key)
		m = out.(chatTUI)
		if got := m.ctrl.ToolApprovalMode(); got != step.mode || m.planMode != step.plan || m.ctrl.PlanMode() != step.plan {
			t.Fatalf("Shift+Tab step %d = mode %q plan %v/%v, want %q/%v", i+1, got, m.planMode, m.ctrl.PlanMode(), step.mode, step.plan)
		}
		if got := m.modeTagText(); got != step.label {
			t.Fatalf("Shift+Tab step %d label = %q, want %q", i+1, got, step.label)
		}
	}
	if got := m.ctrl.Goal(); got != "" {
		t.Fatalf("entering Plan kept Goal %q", got)
	}
}

func TestCtrlYRestoresWorkspaceAfterShiftTabYolo(t *testing.T) {
	m := newTestChatTUI()
	m.ctrl = newOwnedTestController(t, control.Options{})
	for range 2 {
		out, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		m = out.(chatTUI)
	}
	out, _ := m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m = out.(chatTUI)
	if got := m.ctrl.ToolApprovalMode(); got != control.ToolApprovalWorkspaceWrite {
		t.Fatalf("Ctrl+Y restore after Shift+Tab = %q, want workspace-write", got)
	}
}

func TestCtrlYTogglesYoloWithoutLeavingPlan(t *testing.T) {
	m := newTestChatTUI()
	m.ctrl = newOwnedTestController(t, control.Options{})
	m.planMode = true
	m.ctrl.SetPlanMode(true)
	key := tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}
	for i, mode := range []string{control.ToolApprovalDangerFullAccess, control.ToolApprovalReadOnly} {
		out, _ := m.Update(key)
		m = out.(chatTUI)
		if got := m.ctrl.ToolApprovalMode(); got != mode || !m.planMode || !m.ctrl.PlanMode() {
			t.Fatalf("Ctrl+Y step %d in Plan = mode %q plan %v/%v, want %q/true", i+1, got, m.planMode, m.ctrl.PlanMode(), mode)
		}
	}
}
