package serve

import (
	"encoding/json"
	"net/http"

	"reasonix/internal/control"
	"reasonix/internal/event"
)

type exactPromptResolver interface {
	ResolvePromptExact(control.PromptIdentity, control.PromptAnswer) error
}

func (s *Server) resolvePromptExact(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID    string `json:"sessionId"`
		PromptID     string `json:"promptId"`
		TurnID       string `json:"turnId"`
		RuntimeEpoch string `json:"runtimeEpoch"`
		Kind         string `json:"kind"`
		Answer       struct {
			Questions []struct {
				QuestionID string   `json:"questionId"`
				Selected   []string `json:"selected"`
			} `json:"questions"`
			Allow              bool           `json:"allow"`
			Session            bool           `json:"session"`
			Persist            bool           `json:"persist"`
			Action             string         `json:"action"`
			Feedback           string         `json:"feedback"`
			Content            map[string]any `json:"content"`
			Generation         uint64         `json:"generation"`
			PermissionRevision uint64         `json:"permissionRevision"`
		} `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PromptID == "" || body.TurnID == "" || body.Kind == "" {
		http.Error(w, "missing exact prompt identity", http.StatusBadRequest)
		return
	}
	resolver, ok := s.ctl().(exactPromptResolver)
	if !ok {
		http.Error(w, "exact prompt resolution is unavailable", http.StatusConflict)
		return
	}
	if reader, ok := s.ctl().(interface {
		RuntimeStateSnapshot() event.RuntimeStateSnapshot
	}); ok {
		if current := reader.RuntimeStateSnapshot().SessionID; current != "" && body.SessionID != current {
			http.Error(w, "prompt session binding is stale", http.StatusConflict)
			return
		}
	}
	questions := make([]event.AskAnswer, len(body.Answer.Questions))
	for i, question := range body.Answer.Questions {
		questions[i] = event.AskAnswer{QuestionID: question.QuestionID, Selected: question.Selected}
	}
	answer := control.PromptAnswer{Questions: questions, Allow: body.Answer.Allow, Session: body.Answer.Session,
		Persist: body.Answer.Persist, Action: body.Answer.Action, Feedback: body.Answer.Feedback,
		Content: body.Answer.Content, Generation: body.Answer.Generation, PermissionRevision: body.Answer.PermissionRevision}
	identity := control.PromptIdentity{PromptID: body.PromptID, TurnID: body.TurnID, RuntimeEpoch: body.RuntimeEpoch, Kind: control.PromptKind(body.Kind)}
	if err := resolver.ResolvePromptExact(identity, answer); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
