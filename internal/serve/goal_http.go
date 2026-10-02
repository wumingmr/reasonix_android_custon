package serve

import (
	"encoding/json"
	"net/http"
	"strings"
)

// goal sets or clears the active goal. An empty goal string clears it.
// Setting a non-empty goal disables plan mode (matching the desktop behavior).
func (s *Server) goal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Goal string `json:"goal"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	goal := strings.TrimSpace(body.Goal)
	ctrl := s.ctl()
	if err := ctrl.SetGoalDurable(goal); err != nil {
		http.Error(w, "persist goal: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	if goal != "" {
		// Disable plan mode only after the goal mutation has been accepted.
		ctrl.SetPlanMode(false)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) goalEdit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Objective     string  `json:"objective"`
		MaxGoalRounds *uint64 `json:"maxGoalRounds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if err := s.ctl().EditGoalDurable(body.Objective, body.MaxGoalRounds); err != nil {
		http.Error(w, "edit goal: "+err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
