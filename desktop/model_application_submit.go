package main

import (
	"fmt"

	"reasonix/internal/control"
	"reasonix/internal/event"
)

// StartTurnWithModelApplication is an additive host entry point. The choice is
// admission metadata, never part of the model input or submission fingerprint.
func (a *App) StartTurnWithModelApplication(tabID, submissionID string, req control.SubmissionRequest, choice control.ModelApplicationChoice) (TurnStartView, error) {
	return a.startModelApplicationTurn(tabID, submissionID, req, &choice, nil)
}

func (a *App) startModelApplicationTurn(tabID, submissionID string, req control.SubmissionRequest, choice *control.ModelApplicationChoice, target *attachmentTarget) (TurnStartView, error) {
	if submissionID == "" {
		return TurnStartView{}, fmt.Errorf("submissionId is required")
	}
	req.ID = submissionID
	if receipt, found, err := a.knownSubmissionReceipt(tabID, req); found || err != nil {
		return TurnStartView{TurnID: receipt.TurnID, SubmissionID: submissionID, Status: event.TurnQueued, Disposition: control.SubmitTurnStarted}, err
	}
	admission, ctrl, err := a.beginRuntimeTurnWithModelChoice(tabID, true, false, identifiedSubmissionCheck([]string{submissionID}), choice, submissionID)
	if err != nil {
		return TurnStartView{}, a.submissionAdmissionError(tabID, req, err)
	}
	defer admission.abort()
	c, ok := ctrl.(*control.Controller)
	if !ok {
		return TurnStartView{}, a.submissionAdmissionError(tabID, req, fmt.Errorf("structured submission is unsupported"))
	}
	if target != nil && ctrl == target.ctrl && !a.attachmentTargetCurrent(*target) {
		return TurnStartView{}, a.submissionAdmissionError(tabID, req, fmt.Errorf("attachment target changed; please retry"))
	}
	if target != nil && ctrl != target.ctrl {
		_, before, exclusiveBefore := exclusiveSessionBinding(target.ctrl)
		_, after, exclusiveAfter := exclusiveSessionBinding(ctrl)
		if !exclusiveBefore || !exclusiveAfter || before != after || admission.tab != target.tab {
			return TurnStartView{}, a.submissionAdmissionError(tabID, req, fmt.Errorf("attachment target changed; please retry"))
		}
	}
	prepared, err := c.PrepareSubmission(a.reqCtx(), req)
	if err != nil {
		return TurnStartView{}, a.submissionAdmissionError(tabID, req, err)
	}
	setup := func() error {
		if req.Goal != "" {
			if err := syncTabGoalToController(ctrl, req.Goal); err != nil {
				return err
			}
			a.mu.Lock()
			admission.tab.goal = req.Goal
			admission.tab.toolApprovalMode = normalizeToolApprovalMode(req.ToolApprovalMode)
			admission.tab.mode = tabModeFromAxes(false, admission.tab.toolApprovalMode == control.ToolApprovalYolo)
			a.saveTabsLocked()
			a.mu.Unlock()
			ctrl.SetPlanMode(false)
			applyTabToolApprovalModeToController(ctrl, normalizeToolApprovalMode(req.ToolApprovalMode))
		}
		return a.ensureTabTopicIndexedForUserTurn(admission.tab)
	}
	receipt, err := c.SubmitPreparedWithSetup(a.reqCtx(), prepared, setup)
	if err != nil {
		return TurnStartView{}, err
	}
	admission.finish(ctrl)
	return TurnStartView{TurnID: receipt.TurnID, SubmissionID: submissionID, Status: event.TurnQueued, Disposition: control.SubmitTurnStarted}, nil
}

func validateModelApplicationChoice(ctrl control.SessionAPI, choice *control.ModelApplicationChoice) (bool, error) {
	if choice == nil || choice.Mode == "latest" {
		return false, nil
	}
	if choice.Mode != "applied_once" {
		return false, fmt.Errorf("unsupported model application choice")
	}
	concrete, ok := ctrl.(*control.Controller)
	if !ok {
		return false, fmt.Errorf("model application choice is unsupported")
	}
	if err := concrete.ValidateModelApplicationChoice(*choice); err != nil {
		return false, newModelApplicationError(ctrl, err)
	}
	return true, nil
}
