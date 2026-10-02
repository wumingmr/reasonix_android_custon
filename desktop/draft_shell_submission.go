package main

import (
	"errors"
	"reasonix/internal/control"
)

// RunShellForTabWithID uses the same durable admission as model turns. The
// compatibility shell entry point remains available to existing callers.
func (a *App) runShellForTabWithID(tabID, command, submissionID string) error {
	req := control.SubmissionRequest{ID: submissionID, Action: "shell", Input: command, Display: command}
	if found, err := a.knownSubmission(tabID, req); found || err != nil {
		return err
	}
	admission, api, err := a.beginTabTurn(tabID, true, submissionID)
	if err != nil {
		return a.submissionAdmissionError(tabID, req, err)
	}
	defer admission.abort()
	ctrl, ok := api.(*control.Controller)
	if !ok {
		return errors.New("durable shell submission is unavailable")
	}
	if err := a.ensureTabTopicIndexedForUserTurn(admission.tab); err != nil {
		return err
	}
	if _, err := ctrl.SubmitIdentified(req); err != nil {
		return err
	}
	admission.finish(ctrl)
	return nil
}
