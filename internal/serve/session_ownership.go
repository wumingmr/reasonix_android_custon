package serve

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// This file answers who owns a session and renders the read-only views a
// spectator sees; session_handoff.go and session_reclaim.go move ownership.
// doc.go states the single-writer protocol all three implement.

// handoffMode selects how a takeover deals with a turn still running on the
// side that is losing the session.
type handoffMode string

const (
	handoffModeWait      handoffMode = "wait"      // drain: wait for the active turn to finish
	handoffModeInterrupt handoffMode = "interrupt" // cancel the active turn, then hand off
)

const (
	// handoffDefaultTimeout bounds a drain-mode takeover and a reclaim's wait
	// for the local writer to yield.
	handoffDefaultTimeout = 60 * time.Second
	handoffPollInterval   = 200 * time.Millisecond
	// mirrorStaleAfter is how long a mirrored session goes without writer
	// contact (frames or heartbeats) before Serve is willing to probe whether
	// the writer is gone and auto-reclaim. Generous against laptop sleeps and
	// GC pauses; the lease probe is the real authority.
	mirrorStaleAfter       = 30 * time.Second
	externalFramesMaxBody  = 8 << 20
	externalFramesMaxCount = 512
)

// leaseHeldByForeignRuntime probes whether a runtime other than this Serve
// process holds a session's lease. It is a variable only so tests can model
// the local writer as a separate process: the real probe answers false for
// leases held by the calling process, and tests hold the writer's lease
// in-process.
var leaseHeldByForeignRuntime = agent.SessionLeaseHeldByOtherRuntime

// errSessionTakenOver is the stable refusal every mutating endpoint returns
// while the foreground session is mirrored to a local writer. Clients match
// the leading sentence to surface the read-only state.
const errSessionTakenOver = "session is taken over by a local Reasonix window and is read-only here; use POST /reclaim to take it back"

// mirroredSession is Serve's bookkeeping for a session whose lease a local
// runtime now holds. Serve answers reads from the transcript file and mirrors
// the writer's frames to subscribers, but must not mutate the session.
type mirroredSession struct {
	path             string
	mirrorID         string
	handoffID        string
	returnHandoffID  string
	sourceWriterID   string
	targetWriterID   string
	phase            mirrorPhase
	since            time.Time
	lastContact      time.Time
	reclaimRequested bool
	reclaimMode      handoffMode
}

type mirrorPhase string

const (
	mirrorPhasePending          mirrorPhase = "pending"
	mirrorPhaseExternal         mirrorPhase = "external"
	mirrorPhaseReclaimRequested mirrorPhase = "reclaim_requested"
	mirrorPhaseRecovering       mirrorPhase = "recovering"
)

type mirrorGrant struct {
	SessionPath     string `json:"sessionPath"`
	MirrorID        string `json:"mirrorId"`
	HandoffID       string `json:"handoffId,omitempty"`
	ReturnHandoffID string `json:"returnHandoffId"`
	SourceWriterID  string `json:"sourceWriterId"`
	TargetWriterID  string `json:"targetWriterId"`
	Status          string `json:"status"`
}

func newMirrorGeneration() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func newMirroredSession(path, sourceWriterID, targetWriterID string, phase mirrorPhase) (mirroredSession, error) {
	mirrorID, err := newMirrorGeneration()
	if err != nil {
		return mirroredSession{}, err
	}
	handoffID, err := newMirrorGeneration()
	if err != nil {
		return mirroredSession{}, err
	}
	returnHandoffID, err := newMirrorGeneration()
	if err != nil {
		return mirroredSession{}, err
	}
	now := time.Now()
	return mirroredSession{
		path: path, mirrorID: mirrorID, handoffID: handoffID,
		returnHandoffID: returnHandoffID, sourceWriterID: sourceWriterID,
		targetWriterID: targetWriterID, phase: phase, since: now, lastContact: now,
	}, nil
}

func (m mirroredSession) grant(status string) mirrorGrant {
	return mirrorGrant{
		SessionPath: m.path, MirrorID: m.mirrorID, HandoffID: m.handoffID,
		ReturnHandoffID: m.returnHandoffID, SourceWriterID: m.sourceWriterID,
		TargetWriterID: m.targetWriterID, Status: status,
	}
}

// mirrorKey normalizes a session reference for the mirror registry. It is the
// broadcaster's route rule: final-format identity routes key verbatim, legacy
// transcript paths keep the canonical-path form the registry has always used,
// so a mirror entry and the frames emitted about it always agree on the key.
func mirrorKey(path string) string {
	return sessionRouteKey(path)
}

func (s *Server) markMirrored(m mirroredSession) {
	path := mirrorKey(m.path)
	if path == "" {
		return
	}
	m.path = path
	s.mirrorMu.Lock()
	if s.mirrored == nil {
		s.mirrored = map[string]mirroredSession{}
	}
	s.mirrored[path] = m
	s.mirrorMu.Unlock()
}

func (s *Server) clearMirrored(path, mirrorID string) (mirroredSession, bool) {
	path = mirrorKey(path)
	s.mirrorMu.Lock()
	m, ok := s.mirrored[path]
	if !ok || (mirrorID != "" && m.mirrorID != mirrorID) {
		s.mirrorMu.Unlock()
		return mirroredSession{}, false
	}
	delete(s.mirrored, path)
	s.mirrorMu.Unlock()
	return m, true
}

func (s *Server) mirroredEntry(path string) (mirroredSession, bool) {
	path = mirrorKey(path)
	s.mirrorMu.Lock()
	defer s.mirrorMu.Unlock()
	m, ok := s.mirrored[path]
	return m, ok
}

func (s *Server) sessionMirrored(path string) bool {
	_, ok := s.mirroredEntry(path)
	return ok
}

// foregroundMirroredLocked reports whether the current foreground session has
// been handed to a local writer. Callers hold bindMu.
func (s *Server) foregroundMirroredLocked() bool {
	cur := s.ctl()
	if cur == nil {
		return false
	}
	if path := cur.SessionPath(); path != "" {
		return s.sessionMirrored(path)
	}
	// Exclusive identities have no live path; the foreground is mirrored when
	// its bound session ref matches a mirrored identity route.
	if concrete, ok := cur.(*control.Controller); ok {
		if ref, bound := concrete.SessionRef(); bound {
			return s.sessionMirrored(remoteSessionIDQueryPrefix + ref.SessionID)
		}
	}
	return false
}

func (s *Server) touchMirrored(path, mirrorID string, phase mirrorPhase) (mirroredSession, bool) {
	s.mirrorMu.Lock()
	key := mirrorKey(path)
	m, ok := s.mirrored[key]
	if ok && m.mirrorID == mirrorID {
		m.lastContact = time.Now()
		if phase != "" {
			m.phase = phase
		}
		s.mirrored[key] = m
	}
	s.mirrorMu.Unlock()
	return m, ok && m.mirrorID == mirrorID
}

// rejectMirroredForegroundLocked answers 409 for foreground mutations while
// the session is mirrored. Returns true when the response was written.
// Callers hold bindMu.
func (s *Server) rejectMirroredForegroundLocked(w http.ResponseWriter) bool {
	if !s.foregroundMirroredLocked() {
		return false
	}
	http.Error(w, errSessionTakenOver, http.StatusConflict)
	return true
}

// snapshotForeground persists the foreground session before a switch, unless a
// local writer owns it — a save attempt there fails closed (no write
// authority) and the conflict path could fork a recovery branch into a file
// the writer now owns. Callers hold bindMu.
func (s *Server) snapshotForeground(cur control.SessionAPI) {
	if s.foregroundMirroredLocked() {
		return
	}
	if err := cur.Snapshot(); err != nil {
		slog.Warn("serve: snapshot before switch", "err", err)
	}
}

type ownershipView struct {
	SessionPath      string `json:"sessionPath"`
	Holder           string `json:"holder"` // serve | external | other | free
	RemoteAttached   bool   `json:"remoteAttached"`
	Running          bool   `json:"running"`
	Mirrored         bool   `json:"mirrored"`
	ReclaimRequested bool   `json:"reclaimRequested"`
	TakenOver        bool   `json:"takenOver"`
	HolderPID        int    `json:"holderPid,omitempty"`
	HolderHost       string `json:"holderHost,omitempty"`
}

// isSessionIDRoute reports whether a client-supplied session reference names a
// final-format identity instead of a legacy transcript path.
func isSessionIDRoute(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), remoteSessionIDQueryPrefix)
}

// resolveSessionIdentity validates a final-format identity route against this
// serve's session service and resolves its on-disk directory. The directory
// backs the writer-lock occupancy probe; opening the session is never needed
// to answer "who holds it".
func (s *Server) resolveSessionIdentity(raw string) (session.SessionRef, string, error) {
	id, ok := strings.CutPrefix(strings.TrimSpace(raw), remoteSessionIDQueryPrefix)
	if !ok || id == "" {
		return session.SessionRef{}, "", errors.New("invalid session identity")
	}
	concrete, ok := s.ctl().(*control.Controller)
	if !ok {
		return session.SessionRef{}, "", errors.New("session identity protocol is unavailable")
	}
	service := concrete.SessionService()
	if service == nil {
		return session.SessionRef{}, "", errors.New("session service is unavailable")
	}
	ref := session.SessionRef{HostID: service.HostID(), SessionID: id}
	dir, err := service.SessionDir(context.Background(), ref)
	if err != nil {
		return session.SessionRef{}, "", fmt.Errorf("unknown session: %w", err)
	}
	return ref, dir, nil
}

// ownershipIdentity answers the takeover probe for a final-format identity:
// the mirror registry first (external), then the foreground binding (serve),
// then the raw writer lock (other vs free).
func (s *Server) ownershipIdentity(w http.ResponseWriter, raw string) {
	route := strings.TrimSpace(raw)
	ref, dir, err := s.resolveSessionIdentity(route)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	view := ownershipView{SessionPath: route, RemoteAttached: s.bc.Subscribers() > 0}
	if m, ok := s.mirroredEntry(route); ok {
		view.Holder = "external"
		view.Mirrored = true
		view.TakenOver = true
		view.ReclaimRequested = m.reclaimRequested
		s.appendServeIdentity(&view)
		writeJSON(w, view)
		return
	}
	if cur, ok := s.ctl().(*control.Controller); ok {
		if current, bound := cur.SessionRef(); bound && current == ref {
			view.Holder = "serve"
			view.Running = controllerHasActiveRuntimeWork(cur)
			s.appendServeIdentity(&view)
			writeJSON(w, view)
			return
		}
	}
	if d := s.detachedIdentityHolder(ref); d != nil {
		view.Holder = "serve"
		view.Running = controllerHasActiveRuntimeWork(d.ctrl)
		s.appendServeIdentity(&view)
		writeJSON(w, view)
		return
	}
	if session.ProbeWriterHeld(dir) {
		view.Holder = "other"
	} else {
		view.Holder = "free"
	}
	writeJSON(w, view)
}

// detachedIdentityHolder returns the background session bound to ref, if any.
// A detached legacy controller keeps its transcript-path registry key after
// upgrading to an identity mid-turn, so the registry must be scanned by bound
// identity rather than looked up by path. Only detachedMu is taken; callers
// may or may not hold bindMu.
func (s *Server) detachedIdentityHolder(ref session.SessionRef) *detachedSession {
	s.detachedMu.Lock()
	defer s.detachedMu.Unlock()
	for _, d := range s.detached {
		if controllerBoundToIdentity(d.ctrl, ref.SessionID) {
			return d
		}
	}
	return nil
}

// ownership reports who currently writes a session, whether a remote SSE
// client is attached, and whether a turn is running — the inputs a local
// takeover prompt needs. remoteAttached counts every SSE subscriber; Serve
// cannot distinguish the desktop pump from a browser tab, so it is an
// over-approximation of "the remote side is watching".
func (s *Server) ownership(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("session")
	if isSessionIDRoute(raw) {
		s.ownershipIdentity(w, raw)
		return
	}
	realPath, err := s.resolveSessionPath(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	view := ownershipView{SessionPath: agent.CanonicalSessionPath(realPath), RemoteAttached: s.bc.Subscribers() > 0}
	if m, ok := s.mirroredEntry(realPath); ok {
		view.Holder = "external"
		view.Mirrored = true
		view.TakenOver = true
		view.ReclaimRequested = m.reclaimRequested
		s.appendServeIdentity(&view)
		writeJSON(w, view)
		return
	}
	cur := s.ctl()
	foreground := cur != nil && agent.CanonicalSessionPath(cur.SessionPath()) == agent.CanonicalSessionPath(realPath)
	if foreground {
		view.Holder = "serve"
		view.Running = controllerHasActiveRuntimeWork(cur)
		s.appendServeIdentity(&view)
		writeJSON(w, view)
		return
	}
	if s.detachedBusy(realPath) {
		view.Holder = "serve"
		view.Running = s.detachedHasActiveWork(realPath)
		s.appendServeIdentity(&view)
		writeJSON(w, view)
		return
	}
	if leaseHeldByForeignRuntime(realPath) {
		view.Holder = "other"
	}
	if view.Holder == "" {
		view.Holder = "free"
	}
	writeJSON(w, view)
}

func (s *Server) appendServeIdentity(view *ownershipView) {
	host, _ := os.Hostname()
	view.HolderPID = os.Getpid()
	view.HolderHost = strings.TrimSpace(host)
}

func (s *Server) detachedHasActiveWork(path string) bool {
	path = agent.CanonicalSessionPath(path)
	s.detachedMu.Lock()
	defer s.detachedMu.Unlock()
	d := s.detached[path]
	return d != nil && controllerHasActiveRuntimeWork(d.ctrl)
}

type externalFramesRequest struct {
	SessionPath string            `json:"sessionPath"`
	MirrorID    string            `json:"mirrorId"`
	Frames      []eventwire.Event `json:"frames"`
}

type externalFramesResponse struct {
	ReclaimRequested bool        `json:"reclaimRequested"`
	ReclaimMode      handoffMode `json:"reclaimMode,omitempty"`
	ReturnHandoffID  string      `json:"returnHandoffId,omitempty"`
	SourceWriterID   string      `json:"sourceWriterId,omitempty"`
}

// externalFrames mirrors the local writer's frames to every subscriber. An
// empty frame list is a heartbeat: the response tells the writer when the
// remote side asked for the session back, so an idle writer learns about a
// reclaim without pushing anything.
func (s *Server) externalFrames(w http.ResponseWriter, r *http.Request) {
	var body externalFramesRequest
	if err := decodeTakeoverJSON(w, r, &body); err != nil || strings.TrimSpace(body.SessionPath) == "" || strings.TrimSpace(body.MirrorID) == "" {
		if err == nil {
			http.Error(w, "missing sessionPath or mirrorId", http.StatusBadRequest)
		}
		return
	}
	if len(body.Frames) > externalFramesMaxCount {
		http.Error(w, "too many frames", http.StatusRequestEntityTooLarge)
		return
	}
	// Final-format identity routes key the registry verbatim; legacy references
	// still validate as transcript paths inside the session dir.
	if isSessionIDRoute(body.SessionPath) {
		if _, _, err := s.resolveSessionIdentity(body.SessionPath); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		realPath, err := s.resolveSessionPath(body.SessionPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body.SessionPath = agent.CanonicalSessionPath(realPath)
	}
	canonical := mirrorKey(body.SessionPath)
	mirrorID := strings.TrimSpace(body.MirrorID)
	// Validate, publish and advance contact under one mirror generation lock:
	// re-adopt rotates the token under the same lock, so a stale request cannot
	// pass validation and still emit frames before the post-check notices it.
	s.mirrorMu.Lock()
	m, ok := s.mirrored[canonical]
	if !ok || m.mirrorID != mirrorID {
		s.mirrorMu.Unlock()
		http.Error(w, "session is not mirrored by this serve process", http.StatusConflict)
		return
	}
	m.lastContact = time.Now()
	m.phase = mirrorPhaseExternal
	s.mirrored[canonical] = m
	for i := range body.Frames {
		frame := body.Frames[i]
		frame.SessionPath = canonical
		s.bc.EmitWire(frame)
	}
	s.mirrorMu.Unlock()
	writeJSON(w, externalFramesResponse{
		ReclaimRequested: m.reclaimRequested, ReclaimMode: m.reclaimMode,
		ReturnHandoffID: m.returnHandoffID, SourceWriterID: m.sourceWriterID,
	})
}

func decodeTakeoverJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, externalFramesMaxBody))
	if err := decoder.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid request body", http.StatusBadRequest)
		}
		return err
	}
	return nil
}

// identityStatusView renders the status payload for a final-format identity
// selected via ?session-id:...: when another runtime owns the writer, nothing
// can run here and the surface must render read-only. dir backs the writer
// occupancy probe for holders that never adopted.
func (s *Server) identityStatusView(ref session.SessionRef, dir string) map[string]any {
	sess := map[string]any{
		"label":            s.ctl().Label(),
		"running":          false,
		"plan":             false,
		"autoApproveTools": false,
		"bypass":           false,
		"toolApprovalMode": control.ToolApprovalReadOnly,
		"cwd":              s.ctl().SessionDir(),
		"pendingPrompt":    false,
		"backgroundJobs":   0,
		"cancelRequested":  false,
		"cancellable":      false,
		"takenOver":        true,
		"hostId":           ref.HostID,
		"sessionId":        ref.SessionID,
	}
	if m, ok := s.mirroredEntry(remoteSessionIDQueryPrefix + ref.SessionID); ok {
		sess["reclaimRequested"] = m.reclaimRequested
	} else if !session.ProbeWriterHeld(dir) {
		// The probe says the writer is already free; report it reclaimable so
		// the surface can re-attach instead of showing a stale read-only badge.
		sess["takenOver"] = false
	}
	return sess
}

// statusIdentityOverride answers the /status session-id route when this serve
// does not authoritatively run the identity: mirrored or foreign-held
// identities get the read-only takeover view. Identities bound to the
// foreground (or with a free writer) return false so the caller falls through
// to the authoritative controller snapshot or re-attaches.
func (s *Server) statusIdentityOverride(w http.ResponseWriter, raw string) bool {
	route := strings.TrimSpace(raw)
	ref, dir, err := s.resolveSessionIdentity(route)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}
	if s.serveHoldsIdentity(ref) {
		return false
	}
	if _, mirrored := s.mirroredEntry(route); mirrored {
		writeJSON(w, s.identityStatusView(ref, dir))
		s.maybeAutoReclaimMirrored(route)
		return true
	}
	if session.ProbeWriterHeld(dir) {
		writeJSON(w, s.identityStatusView(ref, dir))
		return true
	}
	// Neither run here nor written anywhere: a spectator pinned by a writer
	// that has since exited. Answer this route with takenOver=false; a
	// foreign-route snapshot is discarded while pinned and the banner sticks.
	view := s.identityStatusView(ref, dir)
	view["takenOver"] = false
	writeJSON(w, view)
	return true
}

// identityColdHistory reads a final-format identity's committed messages
// without a controller binding. The writer may live in another process; the
// durable event log is read through the session service's read-only path,
// which never takes the writer lease.
func (s *Server) identityColdHistory(raw string) ([]provider.Message, bool) {
	ref, _, err := s.resolveSessionIdentity(raw)
	if err != nil {
		return nil, false
	}
	concrete, ok := s.ctl().(*control.Controller)
	if !ok {
		return nil, false
	}
	query := concrete.SessionService()
	if query == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msgs, err := query.Query().History(ctx, ref)
	if err != nil {
		return nil, false
	}
	return msgs, true
}

// adopt registers a session the local runtime already owns as mirrored, so
// the remote side can watch it read-only and reclaim it. This is the local
// desktop's announcement when it opens a session directly (no takeover — there
// was nothing to hand off): Serve must know about the writer to mediate
// reclaim and mirror the frames. Sessions Serve itself holds are refused —
// those go through /handoff instead.
func (s *Server) adopt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionPath string `json:"sessionPath"`
		WriterID    string `json:"writerId"`
	}
	if err := decodeTakeoverJSON(w, r, &body); err != nil || strings.TrimSpace(body.SessionPath) == "" || strings.TrimSpace(body.WriterID) == "" {
		if err == nil {
			http.Error(w, "missing sessionPath or writerId", http.StatusBadRequest)
		}
		return
	}
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	if isSessionIDRoute(body.SessionPath) {
		// Final-format identities prove ownership by the writer lock itself;
		// there is no lease sidecar to inspect. The claim is accepted only when
		// some local runtime actually holds the writer.
		ref, dir, idErr := s.resolveSessionIdentity(body.SessionPath)
		if idErr != nil {
			http.Error(w, idErr.Error(), resolveSessionPathStatus(idErr))
			return
		}
		if s.serveHoldsIdentity(ref) {
			http.Error(w, "session is held by this serve; use POST /handoff to take it over", http.StatusConflict)
			return
		}
		if !session.ProbeWriterHeld(dir) {
			http.Error(w, "session is not held by the claimed writer", http.StatusConflict)
			return
		}
		writerID := strings.TrimSpace(body.WriterID)
		if existing, ok := s.mirroredEntry(body.SessionPath); ok && existing.targetWriterID != writerID {
			http.Error(w, "session is mirrored by another writer", http.StatusConflict)
			return
		}
		m, err := newMirroredSession(mirrorKey(body.SessionPath), agent.SessionWriterID(), writerID, mirrorPhaseExternal)
		if err != nil {
			http.Error(w, "create mirror generation", http.StatusInternalServerError)
			return
		}
		m.handoffID = ""
		s.markMirrored(m)
		slog.Info("serve: final-format session adopted by local runtime", "session", m.path)
		s.bc.Emit(event.Event{
			Kind:        event.Notice,
			Level:       event.LevelWarn,
			Code:        event.NoticeCodeSessionTakenOver,
			Text:        "This session was taken over by a local Reasonix window and is read-only here.",
			Detail:      "A Reasonix window on this machine opened this session; it keeps streaming here. Use \"take back\" to reclaim it.",
			SessionPath: m.path,
		})
		writeJSON(w, m.grant("adopted"))
		return
	}
	realPath, err := s.resolveSessionPath(body.SessionPath)
	if err != nil {
		http.Error(w, err.Error(), resolveSessionPathStatus(err))
		return
	}
	if s.serveHoldsSession(realPath) {
		http.Error(w, "session is held by this serve; use POST /handoff to take it over", http.StatusConflict)
		return
	}
	info, held, inspectErr := agent.InspectSessionLease(realPath)
	if inspectErr != nil || !held || info == nil || info.WriterID != strings.TrimSpace(body.WriterID) {
		http.Error(w, "session is not held by the claimed writer", http.StatusConflict)
		return
	}
	if existing, ok := s.mirroredEntry(realPath); ok && existing.targetWriterID != info.WriterID {
		http.Error(w, "session is mirrored by another writer", http.StatusConflict)
		return
	}
	m, err := newMirroredSession(agent.CanonicalSessionPath(realPath), agent.SessionWriterID(), info.WriterID, mirrorPhaseExternal)
	if err != nil {
		http.Error(w, "create mirror generation", http.StatusInternalServerError)
		return
	}
	m.handoffID = ""
	s.markMirrored(m)
	slog.Info("serve: session adopted by local runtime", "session", agent.CanonicalSessionPath(realPath))
	s.bc.Emit(event.Event{
		Kind:        event.Notice,
		Level:       event.LevelWarn,
		Code:        event.NoticeCodeSessionTakenOver,
		Text:        "This session was taken over by a local Reasonix window and is read-only here.",
		Detail:      "A Reasonix window on this machine opened this session; it keeps streaming here. Use \"take back\" to reclaim it.",
		SessionPath: agent.CanonicalSessionPath(realPath),
	})
	writeJSON(w, m.grant("adopted"))
}

// mirroredReadView reports whether session is mirrored, and if so builds the
// file-backed read view for read-only endpoints that select a specific
// session.
func (s *Server) mirroredReadView(session string) (string, []provider.Message, bool) {
	realPath, err := s.resolveSessionPath(session)
	if err != nil || !s.sessionMirrored(realPath) {
		return "", nil, false
	}
	msgs, ok := s.mirroredHistory(realPath)
	if !ok {
		return "", nil, false
	}
	return agent.CanonicalSessionPath(realPath), msgs, true
}

// externalReadView serves the file-backed read view for any session a local
// runtime owns — mirrored via /adopt or /handoff, or merely held by another
// process on this machine. A spectator client (remote tab) needs the local
// writer's transcript, not Serve's foreground.
func (s *Server) externalReadView(session string) (string, []provider.Message, bool) {
	if s.sessionMirrored(session) {
		return s.mirroredReadView(session)
	}
	realPath, err := s.resolveSessionPath(session)
	if err != nil || !leaseHeldByForeignRuntime(realPath) {
		return "", nil, false
	}
	msgs, ok := s.mirroredHistory(realPath)
	if !ok {
		return "", nil, false
	}
	return agent.CanonicalSessionPath(realPath), msgs, true
}

// statusViewForPath renders the per-session status payload for the ?session=
// selector. Local-owned sessions (mirrored or foreign-held) report takenOver;
// Serve-owned sessions report takenOver=false so a spectator client clears its
// read-only pin after reclaim or when the session returns to the foreground.
func (s *Server) statusViewForPath(path string, held bool) map[string]any {
	if held {
		return s.externalStatusView(path)
	}
	running := false
	cur := s.ctl()
	if cur != nil && agent.CanonicalSessionPath(cur.SessionPath()) == agent.CanonicalSessionPath(path) {
		running = controllerHasActiveRuntimeWork(cur)
	}
	return map[string]any{
		"label":            s.ctl().Label(),
		"running":          running,
		"plan":             false,
		"autoApproveTools": false,
		"bypass":           false,
		"toolApprovalMode": control.ToolApprovalWorkspaceWrite,
		"cwd":              s.ctl().SessionDir(),
		"pendingPrompt":    false,
		"backgroundJobs":   0,
		"takenOver":        false,
		"sessionName":      strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		"sessionPath":      agent.CanonicalSessionPath(path),
	}
}

// externalStatusView renders the status payload for a session a local runtime
// owns (mirrored or foreign-held): nothing here can run, ownership is external,
// and the surface must render read-only.
func (s *Server) externalStatusView(path string) map[string]any {
	if _, ok := s.mirroredEntry(path); ok {
		return s.mirrorStatusView(path)
	}
	sess := map[string]any{
		"label":            s.ctl().Label(),
		"running":          false,
		"plan":             false,
		"autoApproveTools": false,
		"bypass":           false,
		"toolApprovalMode": control.ToolApprovalReadOnly,
		"cwd":              s.ctl().SessionDir(),
		"pendingPrompt":    false,
		"backgroundJobs":   0,
		"takenOver":        true,
		"sessionName":      strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		"sessionPath":      agent.CanonicalSessionPath(path),
	}
	return sess
}

// mirrorStatusView renders the status payload for a mirrored session selected
// via ?session=: nothing can run here, ownership is external, and the surface
// must render read-only.
func (s *Server) mirrorStatusView(path string) map[string]any {
	m, ok := s.mirroredEntry(path)
	sess := map[string]any{
		"label":            s.ctl().Label(),
		"running":          false,
		"plan":             false,
		"autoApproveTools": false,
		"bypass":           false,
		"toolApprovalMode": control.ToolApprovalReadOnly,
		"cwd":              s.ctl().SessionDir(),
		"pendingPrompt":    false,
		"backgroundJobs":   0,
		"takenOver":        true,
		"sessionName":      strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		"sessionPath":      agent.CanonicalSessionPath(path),
	}
	if ok {
		sess["reclaimRequested"] = m.reclaimRequested
	}
	return sess
}

// mirrorEnd is the local writer's farewell: it has closed its tab and dropped
// the lease, so the remote side can speak again without an explicit reclaim
// round-trip. A writer that still holds the lease is told to release first —
// ending the mirror under a live writer would leave the foreground writable
// in name only (its write authority is gone) and render stale history.
func (s *Server) mirrorEnd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionPath string `json:"sessionPath"`
		MirrorID    string `json:"mirrorId"`
	}
	if err := decodeTakeoverJSON(w, r, &body); err != nil || strings.TrimSpace(body.SessionPath) == "" || strings.TrimSpace(body.MirrorID) == "" {
		if err == nil {
			http.Error(w, "missing sessionPath or mirrorId", http.StatusBadRequest)
		}
		return
	}
	if isSessionIDRoute(body.SessionPath) {
		s.mirrorEndIdentity(w, r, body.SessionPath, strings.TrimSpace(body.MirrorID))
		return
	}
	realPath, err := s.resolveSessionPath(body.SessionPath)
	if err != nil {
		http.Error(w, err.Error(), resolveSessionPathStatus(err))
		return
	}
	m, ok := s.mirroredEntry(realPath)
	if !ok || m.mirrorID != strings.TrimSpace(body.MirrorID) {
		http.Error(w, "mirror generation changed", http.StatusConflict)
		return
	}
	if leaseHeldByForeignRuntime(realPath) {
		http.Error(w, "local writer still holds the session; release it before ending the mirror", http.StatusConflict)
		return
	}
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	current, ok := s.mirroredEntry(realPath)
	if !ok || current.mirrorID != m.mirrorID {
		http.Error(w, "mirror generation changed", http.StatusConflict)
		return
	}
	s.reclaimMirroredLocked(w, realPath, current)
}

// The desktop sends mirror-end at tab close and releases its runtime in the
// same teardown with no ordering guarantee, so the writer lock normally drops
// within milliseconds of the farewell. mirrorEndReleaseWait bounds how long the
// farewell waits for that drop so the remote side is re-owned at once instead
// of sitting read-only until the 30 s stale auto-reclaim notices; a writer that
// keeps the lock past the bound is accepted and left to that fallback.
var (
	mirrorEndReleaseWait = 2 * time.Second
	mirrorEndReleasePoll = 25 * time.Millisecond
	// mirrorEndProbeHookForTest runs after each probe that still sees the
	// writer lock held, so tests can release the writer at a chosen point.
	mirrorEndProbeHookForTest func(attempt int)
)

// awaitIdentityWriterRelease reports whether the identity's writer lock dropped
// within mirrorEndReleaseWait.
func awaitIdentityWriterRelease(dir string) bool {
	deadline := time.Now().Add(mirrorEndReleaseWait)
	for attempt := 0; session.ProbeWriterHeld(dir); attempt++ {
		if mirrorEndProbeHookForTest != nil {
			mirrorEndProbeHookForTest(attempt)
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(mirrorEndReleasePoll)
	}
	return true
}

// mirrorEndIdentity handles the local writer's farewell for a final-format
// identity. The farewell is sent before the live writer releases its runtime,
// so a still-held writer lock is expected, not an error: wait briefly for the
// drop and re-own the identity as the legacy farewell does; a writer that
// outlives the wait is accepted and the stale auto-reclaim (or process exit)
// finishes the return later.
func (s *Server) mirrorEndIdentity(w http.ResponseWriter, r *http.Request, route, mirrorID string) {
	ref, dir, err := s.resolveSessionIdentity(route)
	if err != nil {
		http.Error(w, err.Error(), resolveSessionPathStatus(err))
		return
	}
	m, ok := s.mirroredEntry(route)
	if !ok || m.mirrorID != mirrorID {
		http.Error(w, "mirror generation changed", http.StatusConflict)
		return
	}
	// Wait outside bindMu: blocking every other command for the whole bound
	// would freeze the serve for a writer that is merely slow to exit.
	if !awaitIdentityWriterRelease(dir) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	current, ok := s.mirroredEntry(route)
	if !ok || current.mirrorID != m.mirrorID {
		http.Error(w, "mirror generation changed", http.StatusConflict)
		return
	}
	s.reclaimIdentityLocked(w, r.Context(), route, ref, current)
}

type statusRecorder struct {
	header http.Header
	status int
}

func (w *statusRecorder) Header() http.Header { return w.header }
func (w *statusRecorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(p), nil
}
func (w *statusRecorder) WriteHeader(status int) { w.status = status }

// mirroredHistory reads the transcript file for a mirrored session so
// hydrating and reconciling clients see the local writer's turns, not Serve's
// frozen in-memory copy. Returns false when the file cannot be read; callers
// fall back to the stale in-memory history.
func (s *Server) mirroredHistory(realPath string) ([]provider.Message, bool) {
	loaded, err := agent.LoadSession(realPath)
	if err != nil || loaded == nil {
		return nil, false
	}
	return loaded.Messages, true
}
