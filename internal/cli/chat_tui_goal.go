package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/i18n"
)

func (m *chatTUI) noticeDeprecatedGoalBudget(cmd control.GoalCommand) {
	if cmd.DeprecatedBudgetFlag {
		m.notice(control.GoalBudgetFlagDeprecatedNotice)
	}
}

func activateGoalDriverAfterRebuild(ctrl control.SessionAPI) control.SessionAPI {
	if concrete, ok := ctrl.(*control.Controller); ok {
		concrete.ActivateGoalDriverAfterRebuild()
	}
	return ctrl
}

func (m *chatTUI) setGoalCommand(cmd control.GoalCommand, input string) tea.Cmd {
	canonical, exclusive := m.ctrl.(interface{ UsesExclusiveSession() bool })
	if exclusive && canonical.UsesExclusiveSession() {
		if err := m.ctrl.SetGoalDurable(cmd.Text); err != nil {
			m.echoLocalCommand(input)
			m.notice("goal: " + err.Error())
			return nil
		}
	} else {
		m.ctrl.SetGoalWithResearchMode(cmd.Text, cmd.ResearchMode)
	}
	m.planMode = false
	m.ctrl.SetPlanMode(false)
	m.ctrl.GoalStrict(cmd.Strict)
	if m.ctrl.GoalStatus() != control.GoalStatusRunning {
		m.echoLocalCommand(input)
		return nil
	}
	m.notice(fmt.Sprintf(i18n.M.GoalSetFmt, control.ShortGoalForNotice(m.ctrl.Goal())))
	return m.startTurn("Start pursuing the active goal now.", input, input)
}

func (m *chatTUI) runGoalSubcommand(input string) tea.Cmd {
	cmd, ok := control.ParseGoalCommand(input)
	if !ok {
		m.echoLocalCommand(input)
		m.notice(i18n.M.GoalEmpty)
		return nil
	}
	switch m.noticeDeprecatedGoalBudget(cmd); cmd.Action {
	case control.GoalCommandSet:
		return m.setGoalCommand(cmd, input)
	case control.GoalCommandClear:
		m.echoLocalCommand(input)
		canonical, exclusive := m.ctrl.(interface{ UsesExclusiveSession() bool })
		if exclusive && canonical.UsesExclusiveSession() {
			if err := m.ctrl.SetGoalDurable(""); err != nil {
				m.notice("goal: " + err.Error())
				break
			}
		} else {
			m.ctrl.ClearGoal()
		}
		m.notice(i18n.M.GoalCleared)
	case control.GoalCommandPause:
		m.echoLocalCommand(input)
		if !m.ctrl.PauseGoal() {
			m.notice(i18n.M.GoalNotRunning)
		}
	case control.GoalCommandResume:
		m.echoLocalCommand(input)
		if !m.ctrl.ResumeGoal() {
			m.notice(i18n.M.GoalNotPaused)
		}
	default:
		m.echoLocalCommand(input)
		goal := m.ctrl.Goal()
		if strings.TrimSpace(goal) == "" {
			m.notice(i18n.M.GoalEmpty)
			break
		}
		m.notice(fmt.Sprintf(i18n.M.GoalCurrentFmt, goal))
		rt := m.ctrl.GoalRuntime()
		m.notice(fmt.Sprintf(i18n.M.GoalRuntimeFmt,
			rt.TurnsUsed, rt.RequestsUsed, rt.TokensUsed,
			control.GoalWorkDurationText(rt.WorkDurationMs)))
		if rt.LastReason != "" {
			m.notice(fmt.Sprintf("%s: %s", i18n.M.GoalRuntimeLastReason, rt.LastReason))
		}
		if rt.StopCause != "" {
			m.notice(fmt.Sprintf(i18n.M.GoalPausedFmt, rt.StopCause))
		}
	}
	return nil
}
