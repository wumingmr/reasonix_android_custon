package serve

import (
	"errors"
	"net/http"
	"reasonix/internal/control"
)

func (s *Server) admitModelSubmissionLocked(w http.ResponseWriter, r *http.Request, choice *control.ModelApplicationChoice, input string, identity control.SubmissionRequest) bool {
	if identified, ok := s.ctl().(*control.Controller); ok && identity.ID != "" && !isServeManagementCommand(input) {
		if _, found, err := identified.LookupSubmission(identity); found || err != nil {
			if err != nil {
				writeSubmissionFailure(w, err)
			} else {
				w.WriteHeader(http.StatusAccepted)
			}
			return false
		}
	}
	if choice != nil && choice.Mode != "latest" && choice.Mode != "applied_once" {
		http.Error(w, "unsupported model application mode", http.StatusBadRequest)
		return false
	}
	if choice != nil && choice.Mode == "applied_once" {
		if s.rejectMirroredForegroundLocked(w) {
			return false
		}
		if err := s.validateAppliedModelChoiceLocked(r.Context(), *choice); err != nil {
			s.rejectModelApplication(w, r, err)
			return false
		}
		return true
	}
	return s.admitModelSettingsRunLocked(w, r)
}

// A returned error does not prove that durable admission failed. Receipt reads
// and post-admission flushes can fail after execution acquired its identity.
// Only the controller's explicit pre-admission sentinel permits a new ID.
func writeSubmissionFailure(w http.ResponseWriter, err error) {
	outcome := "unknown"
	if errors.Is(err, control.ErrSubmissionNotAccepted) {
		outcome = "not_accepted"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	writeJSON(w, map[string]any{"message": err.Error(), "data": map[string]any{"submissionOutcome": outcome}})
}
