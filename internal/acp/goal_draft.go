package acp

import (
	"errors"
	"strings"

	"reasonix/internal/control"
)

func loadedGoalDraftMode(goal string) bool {
	return strings.TrimSpace(goal) == ""
}

func selectedGoalDraftMode(modeID, goal string) bool {
	return modeID == sessionModeGoal && loadedGoalDraftMode(goal)
}

func setACPGoalDurably(ctrl acpController, objective string) error {
	if ctrl == nil {
		return errors.New("session controller is unavailable")
	}
	if setter, ok := ctrl.(interface{ SetGoalDurable(string) error }); ok {
		return setter.SetGoalDurable(objective)
	}
	ctrl.SetGoal(objective)
	return nil
}

func applyACPSessionMode(ctrl acpController, requested string) (nextMode, legacyApproval string, rpcErr *RPCError) {
	nextMode = requested
	clearGoal := false
	switch requested {
	case sessionModeNormal, sessionModePlan:
		clearGoal = true
	case sessionModeGoal:
	case sessionModeLegacyDefault:
		nextMode, legacyApproval, clearGoal = sessionModeNormal, control.ToolApprovalReadOnly, true
	case sessionModeLegacyAuto:
		nextMode, legacyApproval, clearGoal = sessionModeNormal, control.ToolApprovalWorkspaceWrite, true
	default:
		return "", "", &RPCError{Code: ErrInvalidParams, Message: "session/set_mode: unknown modeId " + requested}
	}
	if clearGoal {
		if err := setACPGoalDurably(ctrl, ""); err != nil {
			return "", "", &RPCError{Code: ErrInternal, Message: "session/set_mode: persist goal: " + err.Error()}
		}
	}
	ctrl.SetPlanMode(nextMode == sessionModePlan)
	return nextMode, legacyApproval, nil
}

func applyLoadedACPMode(ctrl acpController, savedMode string) (modeID string, goalDraftMode bool) {
	modeID = normalizeACPCollaborationMode(savedMode)
	switch modeID {
	case sessionModePlan:
		ctrl.SetPlanMode(true)
	case sessionModeGoal:
		ctrl.SetPlanMode(false)
		goalDraftMode = loadedGoalDraftMode(ctrl.Goal())
	default:
		if ctrl.GoalStatus() == control.GoalStatusRunning {
			modeID = sessionModeGoal
		} else {
			modeID = sessionModeNormal
			ctrl.SetPlanMode(false)
		}
	}
	return modeID, goalDraftMode
}

func prepareACPGoalPrompt(sess *acpSession, text string) *RPCError {
	if !sess.takeGoalDraftMode() {
		return nil
	}
	if err := setACPGoalDurably(sess.currentCtrl(), text); err != nil {
		sess.setGoalDraftMode(true)
		return &RPCError{Code: ErrInternal, Message: "session/prompt: persist goal: " + err.Error()}
	}
	sess.saveMetaIfPresent()
	return nil
}

func inheritACPControllerLifecycle(newCtrl *control.Controller, cur acpController) *RPCError {
	prev, ok := cur.(*control.Controller)
	if !ok {
		return nil
	}
	if err := newCtrl.InheritLifecycleFrom(prev); err != nil {
		return &RPCError{Code: ErrInvalidRequest, Message: "session config: active Goal continuation must finish before switching config"}
	}
	newCtrl.RestoreSessionAuthorizations(prev.SessionAuthorizations())
	return nil
}
