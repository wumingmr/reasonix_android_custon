package main

import (
	"errors"
	"fmt"
	"maps"

	"reasonix/internal/control"
	"reasonix/internal/jobs"
)

type ModelApplicationDetails = control.ModelApplicationDetails

func modelApplicationDetails(ctrl control.SessionAPI) *ModelApplicationDetails {
	d := &ModelApplicationDetails{Code: "model_settings_pending", BlockingJobs: []jobs.View{}}
	defer d.SetAvailableActions()
	if c, ok := ctrl.(*control.Controller); ok {
		if scope := c.BackgroundScope(); scope != nil {
			d.Applying = scope.Manager.ReplacementInProgress()
		}
		d.Model = c.ModelRef()
		d.ConnectionTarget = c.ModelConnectionTarget()
		d.RuntimeIdentity = c.ModelRuntimeIdentity()
		var err error
		d.AppliedRevision, d.DesiredRevision, err = c.ModelSettingsState()
		if err != nil {
			d.Code = "model_settings_read_failed"
			return d
		}
		d.BlockingJobs = append(d.BlockingJobs, c.ModelReplacementJobs()...)
		if err := c.ValidateModelContinuation(); err != nil {
			d.ContinuationUnavailable = modelSettingsIssue("continuation_unavailable", err).Message
		} else {
			d.CanUseApplied = true
		}
	}
	return d
}

type modelApplicationError struct {
	cause   error
	details *ModelApplicationDetails
}

func (e *modelApplicationError) Error() string {
	return fmt.Sprintf("model settings were saved but this session could not apply them: %v", e.cause)
}
func (e *modelApplicationError) Unwrap() error { return e.cause }
func (e *modelApplicationError) RPCErrorData() map[string]any {
	return map[string]any{"kind": "model_application", "modelApplication": e.details}
}

func newModelApplicationError(ctrl control.SessionAPI, err error) error {
	d := modelApplicationDetails(ctrl)
	var busy *rebuildBusyError
	if !errors.As(err, &busy) {
		d.Code = "model_settings_apply_failed"
	}
	if errors.Is(err, control.ErrModelChoiceStale) {
		d.Code = "model_choice_stale"
	}
	if errors.Is(err, control.ErrModelContinuationDenied) {
		d.Code = "model_continuation_denied"
	}
	return &modelApplicationError{cause: err, details: d}
}

type submissionNotAcceptedError struct{ cause error }

func (e *submissionNotAcceptedError) Error() string {
	return fmt.Sprintf("submission not accepted\n%v", e.cause)
}
func (e *submissionNotAcceptedError) Unwrap() []error {
	return []error{control.ErrSubmissionNotAccepted, e.cause}
}
func (e *submissionNotAcceptedError) RPCErrorData() map[string]any {
	data := map[string]any{"submissionOutcome": "not_accepted"}
	var detailed interface{ RPCErrorData() map[string]any }
	if errors.As(e.cause, &detailed) {
		maps.Copy(data, detailed.RPCErrorData())
	}
	return data
}
