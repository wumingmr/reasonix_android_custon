package serve

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/session"
)

// reclaim is the remote side's way back: it asks the local writer to yield
// the session, waits for the lease to come free, then re-owns the session.
// The local side demotes passively — it sees reclaimRequested on its next
// frame push or heartbeat — so exactly one side speaks at any moment.
func (s *Server) reclaim(w http.ResponseWriter, r *http.Request) {
	var body handoffRequest
	if err := decodeTakeoverJSON(w, r, &body); err != nil || strings.TrimSpace(body.SessionPath) == "" {
		if err == nil {
			http.Error(w, "missing sessionPath", http.StatusBadRequest)
		}
		return
	}
	if isSessionIDRoute(body.SessionPath) {
		s.reclaimIdentity(w, r, body)
		return
	}
	mode := parseHandoffMode(body.Mode)
	timeout := handoffTimeout(body.TimeoutMs)
	realPath, err := s.resolveSessionPath(body.SessionPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	canonical := agent.CanonicalSessionPath(realPath)

	s.mirrorMu.Lock()
	m, ok := s.mirrored[canonical]
	if !ok {
		s.mirrorMu.Unlock()
		if s.serveHoldsSession(realPath) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// A local holder that never adopted has no mirror forwarder to signal,
		// so the reclaim can only wait for the lease to free. Cap that wait
		// short: the caller needs feedback, not a two-minute hang.
		if leaseHeldByForeignRuntime(realPath) {
			slog.Info("serve: reclaim on un-mirrored foreign-held session (adopter absent)",
				"session", canonical)
			deadline := time.Now().Add(10 * time.Second)
			for leaseHeldByForeignRuntime(realPath) {
				if time.Now().After(deadline) {
					http.Error(w, "session is held by a local Reasonix window that never registered a mirror; close that window or retry after it exits", http.StatusConflict)
					return
				}
				time.Sleep(handoffPollInterval)
			}
			s.bindMu.Lock()
			defer s.bindMu.Unlock()
			s.resumeSession(w, r, realPath)
			return
		}
		http.Error(w, "session is not held by any known runtime", http.StatusConflict)
		return
	}
	m.reclaimRequested = true
	m.reclaimMode = mode
	m.phase = mirrorPhaseReclaimRequested
	s.mirrored[canonical] = m
	s.mirrorMu.Unlock()
	s.bc.Emit(event.Event{
		Kind:        event.Notice,
		Code:        event.NoticeCodeSessionReclaimRequested,
		Text:        "The remote side asked to take this session back.",
		SessionPath: canonical,
	})
	slog.Info("serve: reclaim requested", "session", canonical, "mode", string(mode))

	deadline := time.Now().Add(timeout)
	for leaseHeldByForeignRuntime(realPath) {
		if time.Now().After(deadline) {
			http.Error(w, "local writer did not yield the session; retry", http.StatusConflict)
			return
		}
		time.Sleep(handoffPollInterval)
	}

	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	current, ok := s.mirroredEntry(realPath)
	if !ok || current.mirrorID != m.mirrorID {
		if s.serveHoldsSession(realPath) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "mirror generation changed during reclaim", http.StatusConflict)
		return
	}
	s.reclaimMirroredLocked(w, realPath, current)
}

func (s *Server) serveHoldsSession(realPath string) bool {
	cur := s.ctl()
	if cur != nil && agent.CanonicalSessionPath(cur.SessionPath()) == agent.CanonicalSessionPath(realPath) {
		return true
	}
	return s.detachedBusy(realPath)
}

// serveHoldsIdentity reports whether this serve process runs ref anywhere: on
// the foreground or as a detached background session. The writer-lock probe
// cannot tell the two apart from a foreign holder, so every identity ownership
// answer must consult this first.
func (s *Server) serveHoldsIdentity(ref session.SessionRef) bool {
	if concrete, ok := s.ctl().(*control.Controller); ok {
		if current, bound := concrete.SessionRef(); bound && current == ref {
			return true
		}
	}
	return s.detachedIdentityHolder(ref) != nil
}

// reclaimIdentity is the remote side's way back for a final-format identity:
// ask the mirroring writer to yield, watch the writer lock go free, then
// re-own the session by attaching the foreground through OpenSession.
func (s *Server) reclaimIdentity(w http.ResponseWriter, r *http.Request, body handoffRequest) {
	route := strings.TrimSpace(body.SessionPath)
	mode := parseHandoffMode(body.Mode)
	timeout := handoffTimeout(body.TimeoutMs)
	ref, dir, err := s.resolveSessionIdentity(route)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mirrorMu.Lock()
	m, ok := s.mirrored[mirrorKey(route)]
	if !ok {
		s.mirrorMu.Unlock()
		if s.serveHoldsIdentity(ref) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// A final-format session held by a local writer that never adopted has
		// no mirror forwarder to signal. Wait briefly for the writer lock, then
		// re-own directly if it went free.
		if session.ProbeWriterHeld(dir) {
			slog.Info("serve: reclaim on un-mirrored foreign-held identity (adopter absent)", "session", route)
			deadline := time.Now().Add(10 * time.Second)
			for session.ProbeWriterHeld(dir) {
				if time.Now().After(deadline) {
					http.Error(w, "session is held by a local Reasonix window that never registered a mirror; close that window or retry after it exits", http.StatusConflict)
					return
				}
				time.Sleep(handoffPollInterval)
			}
			s.bindMu.Lock()
			defer s.bindMu.Unlock()
			s.reclaimIdentityLocked(w, r.Context(), route, ref, mirroredSession{})
			return
		}
		http.Error(w, "session is not held by any known runtime", http.StatusConflict)
		return
	}
	m.reclaimRequested = true
	m.reclaimMode = mode
	m.phase = mirrorPhaseReclaimRequested
	s.mirrored[mirrorKey(route)] = m
	s.mirrorMu.Unlock()
	s.bc.Emit(event.Event{
		Kind:        event.Notice,
		Code:        event.NoticeCodeSessionReclaimRequested,
		Text:        "The remote side asked to take this session back.",
		SessionPath: route,
	})
	slog.Info("serve: reclaim requested", "session", route, "mode", string(mode))

	deadline := time.Now().Add(timeout)
	for session.ProbeWriterHeld(dir) {
		if time.Now().After(deadline) {
			http.Error(w, "local writer did not yield the session; retry", http.StatusConflict)
			return
		}
		time.Sleep(handoffPollInterval)
	}

	s.bindMu.Lock()
	defer s.bindMu.Unlock()
	current, ok := s.mirroredEntry(route)
	if !ok || current.mirrorID != m.mirrorID {
		if s.serveHoldsIdentity(ref) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "mirror generation changed during reclaim", http.StatusConflict)
		return
	}
	s.reclaimIdentityLocked(w, r.Context(), route, ref, current)
}

// reclaimIdentityLocked re-owns a final-format identity. OpenSession both
// acquires the writer lease and republishes the foreground; only then does the
// mirror entry clear. Callers hold bindMu. An empty mirror ID marks an
// un-mirrored foreign holder that has since released.
func (s *Server) reclaimIdentityLocked(w http.ResponseWriter, ctx context.Context, route string, ref session.SessionRef, mirror mirroredSession) {
	if mirror.mirrorID != "" {
		s.touchMirrored(route, mirror.mirrorID, mirrorPhaseRecovering)
	}
	concrete, ok := s.ctl().(*control.Controller)
	if !ok || !concrete.UsesExclusiveSession() {
		http.Error(w, "session runtime unavailable", http.StatusInternalServerError)
		return
	}
	if cur, bound := concrete.SessionRef(); !bound || cur != ref {
		if err := concrete.Snapshot(); err != nil {
			http.Error(w, "snapshot current session: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if _, err := concrete.OpenSession(ctx, ref); err != nil {
		if errors.Is(err, session.ErrWriterOwned) {
			http.Error(w, "local writer still holds the session; retry", http.StatusConflict)
		} else {
			http.Error(w, "open session: "+err.Error(), http.StatusConflict)
		}
		return
	}
	// The re-owned identity is the foreground again: refresh the frame tag so
	// live turns carry the reclaimed session's id (the pre-reclaim tag points
	// elsewhere and the desktop pump would drop the frames).
	s.setControllerPath(concrete, "")
	if mirror.mirrorID != "" {
		if _, ok := s.clearMirrored(route, mirror.mirrorID); !ok {
			http.Error(w, "mirror generation changed", http.StatusConflict)
			return
		}
	}
	w.Header().Set(sessionIDHeader, ref.SessionID)
	s.announceSessionChanged("", false)
	s.broadcastReclaimed(route)
	w.WriteHeader(http.StatusNoContent)
	s.replayPendingPromptsBroadcast()
}

// reclaimMirroredLocked acquires the returning writer's reservation, reloads
// and binds the controller, and only then clears the matching mirror epoch.
// Callers hold bindMu.
func (s *Server) reclaimMirroredLocked(w http.ResponseWriter, realPath string, mirror mirroredSession) {
	current, ok := s.mirroredEntry(realPath)
	if !ok || current.mirrorID != mirror.mirrorID {
		http.Error(w, "mirror generation changed", http.StatusConflict)
		return
	}
	s.touchMirrored(realPath, mirror.mirrorID, mirrorPhaseRecovering)
	cur := s.ctl()
	if cur == nil || s.leases == nil {
		http.Error(w, "session runtime unavailable", http.StatusInternalServerError)
		return
	}
	canonical := agent.CanonicalSessionPath(realPath)
	if agent.CanonicalSessionPath(cur.SessionPath()) != canonical && !s.foregroundMirroredLocked() {
		if err := cur.Snapshot(); err != nil {
			http.Error(w, "snapshot current session: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	previous, err := s.acquireReturningLease(realPath, mirror)
	if err != nil {
		if errors.Is(err, agent.ErrSessionLeaseHeld) {
			http.Error(w, sessionInUseError(err), http.StatusConflict)
		} else {
			http.Error(w, "session lease: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}
	committed := false
	defer func() {
		if committed {
			if previous != nil {
				previous.RetireDetached()
			}
			return
		}
		s.rollbackReclaimLease(cur, previous)
	}()
	loaded, err := agent.LoadSession(realPath)
	if err != nil {
		http.Error(w, "load session: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !s.commitLoadedResume(w, cur, loaded, realPath) {
		return
	}
	if _, ok := s.clearMirrored(realPath, mirror.mirrorID); !ok {
		http.Error(w, "mirror generation changed", http.StatusConflict)
		return
	}
	committed = true
	s.bc.ResetSessionPath(realPath)
	s.announceSessionChanged(realPath, false)
	s.broadcastReclaimed(realPath)
	w.WriteHeader(http.StatusNoContent)
	s.replayPendingPromptsBroadcast()
}

// rollbackReclaimLease restores the controller and keeper that were detached
// while a mirrored target was acquired. commitLoadedResume can reject after
// Resume (for example when a test hook rotates the current controller), so the
// source transcript is reloaded and re-authorized before the failed target
// lease is retired.
func (s *Server) rollbackReclaimLease(cur control.SessionAPI, previous *control.SessionLeaseKeeper) {
	failed := s.leases.Split()
	if previous == nil {
		if failed != nil {
			failed.Release()
		}
		return
	}
	previousPath := previous.HeldPath()
	loaded, err := agent.LoadSession(previousPath)
	if err == nil {
		err = previous.BindSessionAuthority(loaded)
	}
	if err == nil {
		cur.Resume(loaded, previousPath)
	} else {
		slog.Error("serve: restore source after failed reclaim", "err", err)
	}
	s.leases.Adopt(previous)
	if ctrl, ok := cur.(*control.Controller); ok && err == nil {
		if bindErr := s.leases.BindControllerAuthority(ctrl); bindErr != nil {
			slog.Error("serve: restore source authority after failed reclaim", "err", bindErr)
		}
	}
	if failed != nil {
		// The same controller may already be restored through s.leases. Retire
		// only the failed target lease without clearing that shared authority.
		failed.RetireDetached()
	}
}

func (s *Server) acquireReturningLease(realPath string, mirror mirroredSession) (*control.SessionLeaseKeeper, error) {
	info, err := agent.LoadSessionLeaseInfo(realPath)
	if err == nil && info != nil && info.HandoffTo == agent.SessionWriterID() &&
		info.HandoffID == mirror.returnHandoffID && info.WriterID == mirror.targetWriterID {
		return s.leases.RebindDetachingWithHandoff(realPath, mirror.targetWriterID, mirror.returnHandoffID)
	}
	return s.leases.RebindDetaching(realPath)
}

func (s *Server) broadcastReclaimed(realPath string) {
	s.bc.Emit(event.Event{
		Kind:        event.Notice,
		Code:        event.NoticeCodeSessionReclaimed,
		Text:        "This session is driven remotely again.",
		SessionPath: mirrorKey(realPath),
	})
}

// maybeAutoReclaimMirrored recovers a mirror whose writer vanished without
// calling /mirror-end (killed window, laptop died). The OS releases the lease
// with the process; once the entry is stale and the lease is free, hand the
// session back to the remote side; a non-nil result closes after the attempt.
func (s *Server) maybeAutoReclaimMirrored(path string) <-chan struct{} {
	m, ok := s.mirroredEntry(path)
	if !ok {
		return nil
	}
	if time.Since(m.lastContact) < mirrorStaleAfter {
		return nil
	}
	if leaseHeldByForeignRuntime(path) {
		// The writer is alive but quiet (or another runtime took the file).
		// Push the staleness window so a chatty-but-healthy writer never
		// gets reclaimed under itself.
		s.touchMirrored(path, m.mirrorID, "")
		return nil
	}
	if m.reclaimRequested {
		// The vanished writer's OS lock is gone, so finish the request. Skipping
		// it leaves the mirror stuck in read-only spectator mode.
		slog.Info("serve: completing outstanding reclaim for vanished writer",
			"session", agent.CanonicalSessionPath(path))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.bindMu.Lock()
		defer s.bindMu.Unlock()
		current, ok := s.mirroredEntry(path)
		if !ok || current.mirrorID != m.mirrorID {
			return
		}
		recorder := &statusRecorder{header: http.Header{}}
		if isSessionIDRoute(path) {
			if ref, _, err := s.resolveSessionIdentity(path); err == nil {
				s.reclaimIdentityLocked(recorder, context.Background(), path, ref, current)
			}
		} else {
			s.reclaimMirroredLocked(recorder, path, current)
		}
		if recorder.status >= http.StatusBadRequest {
			slog.Warn("serve: auto-reclaim of stale mirror failed", "session", path, "status", recorder.status)
			return
		}
		slog.Info("serve: stale mirror auto-reclaimed", "session", path)
	}()
	return done
}
