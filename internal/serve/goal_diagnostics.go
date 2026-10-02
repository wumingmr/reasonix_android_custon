package serve

import (
	"context"
	"io"
	"net/http"

	"reasonix/internal/control"
)

type goalDiagnosticExporter interface {
	ExportGoalDiagnostics(context.Context, control.GoalDiagnosticMetadata) ([]byte, error)
}

type goalDiagnosticStreamExporter interface {
	WriteGoalDiagnostics(context.Context, io.Writer, control.GoalDiagnosticMetadata) error
}

// goalDiagnostics exports the authoritative, fully flushed v3 event stream.
// It is session-fenced but read-only and therefore remains available to a
// spectator inspecting a session owned by another Reasonix surface.
func (s *Server) goalDiagnostics(w http.ResponseWriter, r *http.Request) {
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	if !s.validateExpectedSessionLocked(w, r) {
		return
	}
	controller := s.ctl()
	streamer, streamOK := controller.(goalDiagnosticStreamExporter)
	exporter, exportOK := controller.(goalDiagnosticExporter)
	if !streamOK && !exportOK {
		http.Error(w, "goal diagnostics require goal-lifecycle-v2", http.StatusNotImplemented)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="reasonix-goal-diagnostics.json"`)
	metadata := control.GoalDiagnosticMetadata{Capabilities: s.capabilities()}
	if streamOK {
		if err := streamer.WriteGoalDiagnostics(r.Context(), w, metadata); err != nil {
			// The response may already contain a valid prefix. Closing the body is
			// the only honest signal once streaming has started; never append a
			// second JSON error document to the artifact.
			return
		}
		return
	}
	payload, err := exporter.ExportGoalDiagnostics(r.Context(), metadata)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_, _ = w.Write(payload)
}
