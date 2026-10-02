package serve

import (
	"encoding/json"
	"net/http"
	"reasonix/internal/control"
)

func (s *Server) cancelModelApplicationBlockers(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Choice control.ModelApplicationChoice `json:"choice"`
		IDs    []string                       `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid recovery request", http.StatusBadRequest)
		return
	}
	d := s.modelApplicationDetailsLocked(r.Context())
	if d == nil || d.RuntimeIdentity != body.Choice.ExpectedRuntimeIdentity || d.AppliedRevision != body.Choice.ExpectedAppliedRevision || d.DesiredRevision != body.Choice.ExpectedDesiredRevision {
		s.rejectModelApplication(w, r, control.ErrModelChoiceStale)
		return
	}
	c, ok := s.ctl().(*control.Controller)
	if !ok {
		s.rejectModelApplication(w, r, control.ErrModelChoiceStale)
		return
	}
	c.CancelModelApplicationBlockers(body.IDs)
	s.deferModelApplicationLocked()
	writeJSON(w, map[string]bool{"ok": true})
}
