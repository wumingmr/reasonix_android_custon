package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"reasonix/internal/event"
)

type extensionFormSubmitter interface {
	SubmitExtensionForm(ctx context.Context, pluginID, surfaceID string, values map[string]any) error
}
type exactExtensionFormSubmitter interface {
	SubmitExtensionFormExact(ctx context.Context, pluginID, surfaceID string, generation uint64, formInstanceID string, values map[string]any) error
}

func (s *Server) submitExtensionForm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID      string         `json:"sessionId"`
		PluginID       string         `json:"pluginId"`
		SurfaceID      string         `json:"surfaceId"`
		Generation     uint64         `json:"generation"`
		FormInstanceID string         `json:"formInstanceId"`
		Values         map[string]any `json:"values"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.PluginID) == "" || strings.TrimSpace(body.SurfaceID) == "" {
		http.Error(w, "missing extension form identity", http.StatusBadRequest)
		return
	}
	if body.FormInstanceID != "" {
		submitter, ok := s.ctl().(exactExtensionFormSubmitter)
		if !ok {
			http.Error(w, "exact extension form submission is unavailable", http.StatusConflict)
			return
		}
		if reader, ok := s.ctl().(interface {
			RuntimeStateSnapshot() event.RuntimeStateSnapshot
		}); ok {
			if current := reader.RuntimeStateSnapshot().SessionID; current != "" && body.SessionID != current {
				http.Error(w, "extension form session binding is stale", http.StatusConflict)
				return
			}
		}
		if err := submitter.SubmitExtensionFormExact(r.Context(), body.PluginID, body.SurfaceID, body.Generation, body.FormInstanceID, body.Values); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	submitter, ok := s.ctl().(extensionFormSubmitter)
	if !ok {
		http.Error(w, "extension form submission is unavailable", http.StatusConflict)
		return
	}
	if err := submitter.SubmitExtensionForm(r.Context(), body.PluginID, body.SurfaceID, body.Values); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
