package serve

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"reasonix/internal/control"
	"reasonix/internal/session"
)

// forkSessionBodyMax bounds the /fork-session body: turn id, title, and
// operation id are short strings, so nothing legitimate approaches this limit.
const forkSessionBodyMax = 8 << 10

// forkTargetsResponse is GET /fork-targets' payload. Targets is a JSON array on
// every path, including the empty one: a client renders [] as "nothing to fork
// from", and null would break its list rendering.
type forkTargetsResponse struct {
	Source     session.SessionRef   `json:"source"`
	Targets    []session.ForkTarget `json:"targets"`
	Verifiable bool                 `json:"verifiable"`
}

// forkSessionResponse identifies the child POST /fork-session created. The
// parent keeps its own identity, so this child id is not the serve session id
// the X-Reasonix-Session-ID response header reports and is never echoed there.
type forkSessionResponse struct {
	HostID     string `json:"hostId,omitempty"`
	SessionID  string `json:"sessionId"`
	TurnID     string `json:"turnId"`
	TurnNumber int    `json:"turnNumber"`
}

type forkErrorResponse struct {
	Code    string                   `json:"code"`
	Reason  session.ForkAvailability `json:"reason,omitempty"`
	Message string                   `json:"message"`
}

func writeForkError(w http.ResponseWriter, status int, reason session.ForkAvailability, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(forkErrorResponse{Code: "fork_unavailable", Reason: reason, Message: message})
}

func (s *Server) requireForkSessionFenceLocked(w http.ResponseWriter, r *http.Request) bool {
	identity, ok := s.ctl().(control.IdentityLifecycle)
	if !ok || !identity.UsesExclusiveSession() {
		return true
	}
	if strings.TrimSpace(r.Header.Get(expectedSessionIDHeader)) == "" && strings.TrimSpace(r.Header.Get(expectedSessionPathHeader)) == "" {
		writeForkError(w, http.StatusBadRequest, session.ForkStaleSource, "expected session header is required")
		return false
	}
	return true
}

// registerForkRoutes mounts the fork reads and the parent-preserving child
// creation.
func (s *Server) registerForkRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /fork-targets", s.forkTargets)
	mux.HandleFunc("POST /fork-session", s.forkSession)
}

// forkTargets lists the turns of the current serve session a client may fork
// from, each with the reason it is refused when it is not forkable. The list is
// derived from the session's durable commits, so it is the same list every
// client computes and it stays readable while a turn is running.
func (s *Server) forkTargets(w http.ResponseWriter, r *http.Request) {
	s.bindMu.Lock()
	if !s.requireForkSessionFenceLocked(w, r) {
		s.bindMu.Unlock()
		return
	}
	if err := s.expectedSessionErrorLocked(r); err != nil {
		writeForkError(w, http.StatusConflict, session.ForkStaleSource, err.Error())
		s.bindMu.Unlock()
		return
	}
	ref, service, ok := s.forkSourceLocked(w)
	s.bindMu.Unlock()
	if !ok {
		return
	}
	// A legacy session keeps messages without turn records, so it proves no
	// boundary: the empty, unverifiable set is the honest answer.
	if service == nil {
		writeJSON(w, forkTargetsResponse{Source: ref, Targets: []session.ForkTarget{}})
		return
	}
	// The read runs unlocked: it walks the session log, and holding bindMu for
	// that would stall /resume and /fork behind one client's transcript.
	set, err := service.ForkTargetSetFor(r.Context(), ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if set.Targets == nil {
		set.Targets = []session.ForkTarget{}
	}
	writeJSON(w, forkTargetsResponse{Source: set.Source, Targets: set.Targets, Verifiable: set.Verifiable})
}

// forkSession creates an independent child session from one completed turn of
// the current serve session. Unlike POST /fork it never switches the parent:
// the controller, the session lease, and the broadcast binding stay where they
// are, so a running parent keeps running and keeps its remote viewers. The cut
// is resolved from the source's persisted turn records and must match the
// source identity and atomic boundary the client observed.
func (s *Server) forkSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SourceSessionID  string `json:"sourceSessionId"`
		TurnID           string `json:"turnId"`
		BoundarySequence uint64 `json:"boundarySequence"`
		Name             string `json:"name"`
		OperationID      string `json:"operationId"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, forkSessionBodyMax)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.SourceSessionID) == "" ||
		strings.TrimSpace(body.TurnID) == "" || body.BoundarySequence == 0 || strings.TrimSpace(body.OperationID) == "" {
		writeForkError(w, http.StatusBadRequest, "", "sourceSessionId, turnId, boundarySequence, and operationId are required")
		return
	}
	// Validate and resolve the source under bindMu, then create unlocked: the
	// fork copies the parent's durable log, and holding the binding lock for it
	// would block /resume, /new, and /fork for the whole copy.
	s.bindMu.Lock()
	if !s.requireForkSessionFenceLocked(w, r) {
		s.bindMu.Unlock()
		return
	}
	if err := s.expectedSessionErrorLocked(r); err != nil {
		writeForkError(w, http.StatusConflict, session.ForkStaleSource, err.Error())
		s.bindMu.Unlock()
		return
	}
	// A mirrored foreground is owned by a local writer, so Serve's copy of it is
	// not the transcript a child may inherit (the same refusal as POST /fork).
	if s.foregroundMirroredLocked() {
		writeForkError(w, http.StatusConflict, session.ForkActiveAuthority, errSessionTakenOver)
		s.bindMu.Unlock()
		return
	}
	ref, service, ok := s.forkSourceLocked(w)
	if ok && service != nil && ref.SessionID != strings.TrimSpace(body.SourceSessionID) {
		writeForkError(w, http.StatusConflict, session.ForkStaleSource, "fork source session changed")
		ok = false
	}
	s.bindMu.Unlock()
	if !ok {
		return
	}
	if service == nil {
		writeForkError(w, http.StatusNotImplemented, session.ForkUnsupported, "session forks are unavailable")
		return
	}
	result, err := service.CreateFork(r.Context(), session.ForkRequest{
		Source: ref, TurnID: strings.TrimSpace(body.TurnID), BoundarySequence: body.BoundarySequence,
		OperationID: strings.TrimSpace(body.OperationID),
	})
	if err != nil {
		var unavailable *session.ForkUnavailableError
		if errors.As(err, &unavailable) {
			writeForkError(w, http.StatusConflict, unavailable.Reason, unavailable.Error())
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if title := strings.TrimSpace(body.Name); title != "" {
		// The child is already durable; a title that fails to record is a
		// presentation loss, not a failed creation.
		if err := service.SetTitle(r.Context(), result.Child, title); err != nil {
			slog.Warn("serve: fork child title", "session", result.Child.SessionID, "err", err)
		}
	}
	writeJSON(w, forkSessionResponse{
		HostID: result.Child.HostID, SessionID: result.Child.SessionID,
		TurnID: result.Turn.TurnID, TurnNumber: result.Turn.TurnNumber,
	})
}

// forkSourceLocked resolves the identity session a fork reads from and the
// service that owns it, from one publication epoch. Callers hold bindMu. A nil
// service is a legacy session, which keeps no turn records and so no verifiable
// boundary; ok is false once a refusal has been written.
func (s *Server) forkSourceLocked(w http.ResponseWriter) (session.SessionRef, *session.Service, bool) {
	identity, ok := s.ctl().(control.IdentityLifecycle)
	if !ok || !identity.UsesExclusiveSession() {
		return session.SessionRef{}, nil, true
	}
	ref, bound := identity.SessionRef()
	service := identity.SessionService()
	if !bound || service == nil || service.Query() == nil {
		writeForkError(w, http.StatusConflict, session.ForkStaleSource, "canonical session identity is unavailable")
		return session.SessionRef{}, nil, false
	}
	return ref, service, true
}
