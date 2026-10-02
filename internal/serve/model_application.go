package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/jobs"
)

const modelApplicationCapability = "model-application-v1"

// Validate the source revision immediately before publication. A candidate's
// virtual resolver is immutable, so its local fingerprint alone cannot detect
// a newer save on Desktop while the remote build was in progress.
func validateModelCandidate(ctx context.Context, candidate control.SessionAPI, settings *config.ModelRuntimeSettings) error {
	if changed, err := runtimeModelSettingsChanged(candidate); err != nil {
		return err
	} else if changed {
		return control.ErrModelChoiceStale
	}
	if settings == nil || settings.SourceToken == "" {
		return nil
	}
	offer, err := config.NewModelSettingsOfferID()
	if err != nil {
		return err
	}
	response, err := requestModelSettingsSource(ctx, settings, config.ModelSettingsSourceRequest{Mode: "inspect", OfferID: offer, AppliedRevision: settings.Revision, Model: sourceModelRef(settings, candidate.ModelRef()), OwnedRevisions: []string{}})
	if err != nil {
		return err
	}
	if response.Revision != settings.Revision {
		return control.ErrModelChoiceStale
	}
	return nil
}

func (s *Server) modelApplicationDetailsLocked(ctx context.Context) *control.ModelApplicationDetails {
	c, ok := s.ctl().(*control.Controller)
	if !ok {
		return nil
	}
	d := &control.ModelApplicationDetails{Code: "model_settings_pending", RuntimeIdentity: c.ModelRuntimeIdentity(), Model: c.ModelRef(), BlockingJobs: append([]jobs.View{}, c.ModelReplacementJobs()...)}
	defer d.SetAvailableActions()
	if scope := c.BackgroundScope(); scope != nil {
		d.Applying = scope.Manager.ReplacementInProgress()
	}
	d.ConnectionTarget = c.ModelConnectionTarget()
	var err error
	d.AppliedRevision, d.DesiredRevision, err = c.ModelSettingsState()
	if err != nil {
		d.Code = "model_settings_read_failed"
		return d
	}
	if err = c.ValidateModelContinuation(); err != nil {
		d.ContinuationUnavailable = err.Error()
	} else {
		d.CanUseApplied = true
	}
	if settings := s.managedModels; settings != nil && settings.SourceToken != "" {
		d.AppliedRevision = settings.Revision
		offer, offerErr := config.NewModelSettingsOfferID()
		if offerErr != nil {
			d.CanUseApplied = false
			return d
		}
		response, sourceErr := requestModelSettingsSource(ctx, settings, config.ModelSettingsSourceRequest{Mode: "inspect", OfferID: offer, AppliedRevision: settings.Revision, Model: sourceModelRef(settings, c.ModelRef()), OwnedRevisions: []string{}})
		if sourceErr != nil {
			d.CanUseApplied = false
			d.ContinuationUnavailable = "Desktop cannot verify the current configuration"
			return d
		}
		d.DesiredRevision = response.Revision
		d.ConnectionTarget = response.ConnectionTarget
		d.CanUseApplied = d.CanUseApplied && response.CanContinue
		if !response.CanContinue {
			d.ContinuationUnavailable = response.ContinuationUnavailable
		}
	}
	r := &s.modelApplicationRetry
	r.mu.Lock()
	if r.owner == c && r.failedRevision != "" && r.failedRevision == d.DesiredRevision {
		d.Code = "model_settings_apply_failed"
	}
	r.mu.Unlock()
	return d
}

func (s *Server) validateAppliedModelChoiceLocked(ctx context.Context, choice control.ModelApplicationChoice) error {
	d := s.modelApplicationDetailsLocked(ctx)
	if d == nil || choice.Mode != "applied_once" || d.RuntimeIdentity != choice.ExpectedRuntimeIdentity || d.AppliedRevision != choice.ExpectedAppliedRevision || d.DesiredRevision != choice.ExpectedDesiredRevision {
		return control.ErrModelChoiceStale
	}
	if !d.CanUseApplied {
		return fmt.Errorf("%w: %s", control.ErrModelContinuationDenied, d.ContinuationUnavailable)
	}
	return nil
}

func (s *Server) rejectModelApplication(w http.ResponseWriter, r *http.Request, err error) {
	d := s.modelApplicationDetailsLocked(r.Context())
	if d != nil && !control.ModelReplacementBlocked(s.ctl()) {
		d.Code = "model_settings_apply_failed"
	}
	if d != nil && errors.Is(err, control.ErrModelChoiceStale) {
		d.Code = "model_choice_stale"
	}
	if d != nil && errors.Is(err, control.ErrModelContinuationDenied) {
		d.Code = "model_continuation_denied"
	}
	if d != nil && d.Code == "model_settings_pending" {
		s.deferModelApplicationLocked()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	writeJSON(w, map[string]any{"message": err.Error(), "data": map[string]any{"submissionOutcome": "not_accepted", "kind": "model_application", "modelApplication": d}})
}
