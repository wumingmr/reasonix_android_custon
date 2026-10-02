package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/control"
	"reasonix/internal/session"
	"reasonix/internal/sessionexport"
)

// fixedSessionExportSource resolves one explicit export identity without
// requiring that identity to remain the foreground writer. The service owner,
// and therefore the storage root and HostID, always comes from this process.
func (s *Server) fixedSessionExportSource(w http.ResponseWriter, r *http.Request, snapshotRef session.SessionRef) (*session.Query, session.SessionRef, *control.Controller, bool) {
	s.bindMu.Lock()
	identity, ok := s.ctl().(control.IdentityLifecycle)
	if !ok || !identity.UsesExclusiveSession() {
		s.bindMu.Unlock()
		http.Error(w, "canonical session export is unavailable", http.StatusNotImplemented)
		return nil, session.SessionRef{}, nil, false
	}
	service := identity.SessionService()
	if service == nil || service.Query() == nil {
		s.bindMu.Unlock()
		http.Error(w, "canonical session identity is unavailable", http.StatusConflict)
		return nil, session.SessionRef{}, nil, false
	}
	current, bound := identity.SessionRef()
	controller, _ := s.ctl().(*control.Controller)
	legacyExpected := strings.TrimSpace(r.Header.Get(expectedSessionPathHeader))
	if legacyExpected != "" && !strings.HasPrefix(legacyExpected, remoteSessionIDQueryPrefix) {
		if err := s.expectedSessionPathErrorLocked(legacyExpected); err != nil {
			s.bindMu.Unlock()
			http.Error(w, err.Error(), http.StatusConflict)
			return nil, session.SessionRef{}, nil, false
		}
	}
	query := service.Query()
	hostID := service.HostID()
	s.bindMu.Unlock()

	targetID := ""
	addTarget := func(raw string) bool {
		candidate, err := canonicalSessionExportID(raw)
		if err != nil {
			http.Error(w, "invalid export target identity", http.StatusBadRequest)
			return false
		}
		if candidate == "" {
			return true
		}
		if targetID != "" && targetID != candidate {
			http.Error(w, "export target identities conflict", http.StatusConflict)
			return false
		}
		targetID = candidate
		return true
	}
	if !addTarget(r.URL.Query().Get("sessionId")) ||
		!addTarget(r.Header.Get(expectedSessionIDHeader)) ||
		!addTarget(func() string {
			if strings.HasPrefix(legacyExpected, remoteSessionIDQueryPrefix) {
				return legacyExpected
			}
			return ""
		}()) ||
		!addTarget(snapshotRef.SessionID) {
		return nil, session.SessionRef{}, nil, false
	}
	if snapshotRef.HostID != "" && snapshotRef.HostID != hostID {
		http.Error(w, "export target host changed", http.StatusConflict)
		return nil, session.SessionRef{}, nil, false
	}
	var ref session.SessionRef
	if targetID == "" {
		if !bound {
			http.Error(w, "canonical session identity is unavailable", http.StatusConflict)
			return nil, session.SessionRef{}, nil, false
		}
		ref = current
	} else {
		var err error
		ref, err = query.ResolveSessionID(r.Context(), targetID)
		if err != nil {
			http.Error(w, "session export source is unavailable", http.StatusConflict)
			return nil, session.SessionRef{}, nil, false
		}
	}
	if _, err := query.Stat(r.Context(), ref); err != nil {
		http.Error(w, "session export source is unavailable", http.StatusConflict)
		return nil, session.SessionRef{}, nil, false
	}
	if !bound || current != ref {
		controller = nil
	}
	return query, ref, controller, true
}

// canonicalSessionExportID terminates HTTP taint at the storage identity
// boundary. A remote caller selects an opaque session name; it never supplies
// a path beneath the service-owned storage root.
func canonicalSessionExportID(raw string) (string, error) {
	candidate := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), remoteSessionIDQueryPrefix))
	if candidate == "" {
		return "", nil
	}
	if err := session.ValidateSessionID(candidate); err != nil {
		return "", err
	}
	// filepath.Base is deliberately applied after the stricter cross-platform
	// validator so static analysis and future persistence implementations both
	// receive a single local path component.
	return filepath.Base(candidate), nil
}

func (s *Server) sessionExportSnapshot(w http.ResponseWriter, r *http.Request) {
	query, ref, _, ok := s.fixedSessionExportSource(w, r, session.SessionRef{})
	if !ok {
		return
	}
	var snapshot session.ExportSnapshot
	var err error
	if r.URL.Query().Get("diagnostic") == "1" {
		snapshot, err = query.CaptureDiagnosticSnapshot(r.Context(), ref)
	} else {
		snapshot, err = query.CaptureExportSnapshot(r.Context(), ref)
	}
	if err != nil {
		http.Error(w, "Unable to capture session export", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(snapshot)
}

// The snapshot is a stateless read capability within the authenticated session.
// No remote job/temporary files survive the response, including disconnects.
func (s *Server) sessionExportDocument(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Snapshot session.ExportSnapshot `json:"snapshot"`
		Format   string                 `json:"format"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&request); err != nil {
		http.Error(w, "invalid export request", http.StatusBadRequest)
		return
	}
	documentName := ""
	switch request.Format {
	case "markdown":
		documentName = "markdown"
	case "json":
		documentName = "json"
	case "blocks":
		documentName = "blocks"
	default:
		http.Error(w, "invalid export format", http.StatusBadRequest)
		return
	}
	query, ref, _, ok := s.fixedSessionExportSource(w, r, request.Snapshot.Ref)
	if !ok {
		return
	}
	// Filesystem identity comes only from the canonical binding. Rebuilding the
	// snapshot prevents request data from becoming a path component even if the
	// equality check above is weakened later.
	snapshot := session.ExportSnapshot{
		ReadIncomplete:    request.Snapshot.ReadIncomplete,
		Ref:               ref,
		StorageGeneration: request.Snapshot.StorageGeneration,
		SnapshotSequence:  request.Snapshot.SnapshotSequence,
		AcceptedThrough:   request.Snapshot.AcceptedThrough,
		DurableThrough:    request.Snapshot.DurableThrough,
		CapturedAt:        request.Snapshot.CapturedAt,
		Title:             request.Snapshot.Title,
	}
	dir, err := os.MkdirTemp("", "reasonix-session-export-")
	if err != nil {
		http.Error(w, "export staging failed", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(dir)
	doc, err := sessionexport.BuildForRef(r.Context(), query, ref, snapshot, dir, nil)
	if err != nil {
		http.Error(w, "session export failed or source changed", http.StatusConflict)
		return
	}
	file, err := os.Open(filepath.Join(dir, documentName))
	if err != nil {
		http.Error(w, "export unavailable", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	// Content-Length lets a client distinguish EOF from a truncated transport.
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "export unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("X-Reasonix-Export-Records", fmt.Sprint(doc.Records))
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	_, _ = io.Copy(w, file)
}

func (s *Server) sessionExportDiagnostic(w http.ResponseWriter, r *http.Request) {
	var extra map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&extra); err != nil {
		http.Error(w, "invalid observation", http.StatusBadRequest)
		return
	}
	var snapshot session.ExportSnapshot
	if value, present := extra["exportSnapshot"]; present {
		raw, err := json.Marshal(value)
		if err != nil || json.Unmarshal(raw, &snapshot) != nil {
			http.Error(w, "invalid export snapshot", http.StatusBadRequest)
			return
		}
	}
	query, ref, controller, ok := s.fixedSessionExportSource(w, r, snapshot.Ref)
	if !ok {
		return
	}
	if snapshot.Ref.SessionID == "" {
		var err error
		snapshot, err = query.CaptureDiagnosticSnapshot(r.Context(), ref)
		if err != nil {
			http.Error(w, "unable to capture diagnostic snapshot", http.StatusConflict)
			return
		}
		extra["exportSnapshot"] = snapshot
	}
	s.bindMu.Lock()
	caps := s.capabilities()
	s.bindMu.Unlock()
	// Stage before sending headers so a failed writer never becomes a valid-looking
	// truncated JSON download. Flush failures remain evidence inside the document.
	file, err := os.CreateTemp("", "reasonix-diagnostic-")
	if err != nil {
		http.Error(w, "diagnostics unavailable", http.StatusInternalServerError)
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	allowed := map[string]any{}
	for _, name := range []string{"sessionIdentity", "exportSnapshot", "frontendObservation", "readDiagnostics"} {
		if value, ok := extra[name]; ok {
			allowed[name] = value
		}
	}
	metadata := control.GoalDiagnosticMetadata{Capabilities: caps}
	if s.sessionDiagnosticControllerCurrent(controller, ref) {
		err = controller.WriteSessionDiagnostics(r.Context(), file, metadata, allowed)
		if !s.sessionDiagnosticControllerCurrent(controller, ref) {
			resetErr := file.Truncate(0)
			if resetErr == nil {
				_, resetErr = file.Seek(0, io.SeekStart)
			}
			if resetErr == nil {
				err = control.WriteColdSessionDiagnostics(r.Context(), file, query, snapshot, metadata, allowed)
			} else {
				err = errors.Join(err, resetErr)
			}
		}
	} else {
		err = control.WriteColdSessionDiagnostics(r.Context(), file, query, snapshot, metadata, allowed)
	}
	if err != nil {
		http.Error(w, "diagnostic export failed", http.StatusInternalServerError)
		return
	}
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "diagnostic export failed", http.StatusInternalServerError)
		return
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "diagnostic export failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, file)
}

func (s *Server) sessionDiagnosticControllerCurrent(controller *control.Controller, ref session.SessionRef) bool {
	if controller == nil {
		return false
	}
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	current, ok := s.ctl().(*control.Controller)
	if !ok || current != controller {
		return false
	}
	identity, ok := s.ctl().(control.IdentityLifecycle)
	if !ok {
		return false
	}
	boundRef, bound := identity.SessionRef()
	return bound && boundRef == ref
}

func (s *Server) sessionExportValidate(w http.ResponseWriter, r *http.Request) {
	var snapshot session.ExportSnapshot
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&snapshot); err != nil {
		http.Error(w, "invalid snapshot", http.StatusBadRequest)
		return
	}
	query, ref, _, ok := s.fixedSessionExportSource(w, r, snapshot.Ref)
	if !ok {
		return
	}
	if query.ValidateExportSourceForRef(ref, snapshot) != nil {
		http.Error(w, "export source changed", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
