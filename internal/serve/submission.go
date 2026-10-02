package serve

import (
	"net/http"
	"reasonix/internal/control"
	"reasonix/internal/provider"
	"strings"
)

func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	body, trimmed, ok := decodeSubmitRequest(w, r)
	if !ok {
		return
	}
	// Session rotations must complete while bindMu is held. Controller.Submit
	// dispatches these verbs asynchronously, which would let a following model,
	// resume, or extension command cross the rotation generation boundary.
	switch trimmed {
	case "/new":
		s.newSessionFromSubmit(w, r)
		return
	case "/clear":
		s.clearSessionFromSubmit(w, r)
		return
	}
	// Intercept /model <ref> for runtime model switching (the controller's
	// Submit path only lists models — switching is frontend-specific).
	if s.submitModelCommand(w, r, trimmed) {
		return
	}
	// Intercept /effort <level> for reasoning effort switching.
	if strings.HasPrefix(trimmed, "/effort ") {
		level := strings.TrimSpace(strings.TrimPrefix(trimmed, "/effort"))
		if level != "" {
			if err := s.switchEffortExpected(r.Context(), level, r.Header.Get(expectedSessionPathHeader)); err != nil {
				http.Error(w, err.Error(), runtimeSwitchErrorStatus(err))
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	// Admission and controller replacement share one ownership boundary.
	s.bindMu.Lock()
	// Known durable receipts win before configuration validation on a retry.
	identity := control.SubmissionRequest{ID: body.SubmissionID, HTTP: true, Input: body.Input, Format: body.Format, Action: body.Action, RecoveryID: body.RecoveryID}
	if !s.validateExpectedSessionLocked(w, r) {
		s.bindMu.Unlock()
		return
	}
	if !s.admitModelSubmissionLocked(w, r, body.ModelApplication, trimmed, identity) {
		s.bindMu.Unlock()
		return
	}
	ctrl := s.ctl()
	// Fix false 202 while a turn is active: SubmitHTTPFormat silently drops
	// concurrent input. Clients must use POST /inbox/items for durable follow-up.
	identified, identifiedOK := ctrl.(*control.Controller)
	if ctrl.Running() {
		s.bindMu.Unlock()
		if _, err := control.MaintenanceCommandConflict(ctrl, trimmed); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, SubmitBusyMessage+"; use POST /inbox/items for durable follow-up", http.StatusConflict)
		return
	}
	if body.Action == control.ProtocolRecoveryAction {
		pending, ok := ctrl.(interface {
			PendingProtocolRecovery() *provider.ProtocolRecoveryAction
		})
		var action *provider.ProtocolRecoveryAction
		if ok {
			action = pending.PendingProtocolRecovery()
		}
		if action == nil || action.ID != body.RecoveryID {
			s.bindMu.Unlock()
			http.Error(w, "protocol recovery is unavailable or stale", http.StatusConflict)
			return
		}
	}
	if routing, ok := ctrl.(interface{ SetTurnSubmissionID(string) }); ok {
		routing.SetTurnSubmissionID(body.SubmissionID)
	}
	if identifiedOK && !isServeManagementCommand(trimmed) {
		_, err := identified.SubmitIdentified(identity)
		if err != nil {
			s.bindMu.Unlock()
			writeSubmissionFailure(w, err)
			return
		}
		s.bindMu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		return
	}
	submitWithAction(ctrl, body.Input, body.Format, body.Action, body.RecoveryID)
	if isServeManagementCommand(trimmed) && !ctrl.Running() && !ctrl.RuntimeStatus().PendingPrompt {
		// Management notices/status are successful non-turn operations.
		s.bindMu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Legacy clients without receipts still use the synchronous running gate.
	if !ctrl.Running() && !ctrl.RuntimeStatus().PendingPrompt {
		s.bindMu.Unlock()
		http.Error(w, "input was not admitted; session is rotating, closed, or finishing — use POST /inbox/items", http.StatusConflict)
		return
	}
	s.bindMu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}
