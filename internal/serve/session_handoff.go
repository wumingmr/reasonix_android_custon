package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/session"
)

type handoffRequest struct {
	SessionPath    string `json:"sessionPath"`
	TargetWriterID string `json:"targetWriterId"`
	Force          bool   `json:"force"`
	Mode           string `json:"mode"`
	TimeoutMs      int    `json:"timeoutMs"`
}

// handoff releases a session Serve holds so a local runtime on this machine
// can take it over. With force unset it refuses while a remote client is
// attached — the caller is expected to have confirmed the takeover with its
// user via GET /ownership. wait drains a running turn; interrupt cancels it.
// Final-format identities may arrive as a "session-id:<id>" route or the
// explicit sessionId field; both take the identity release path, which swaps
// the writer lease instead of a path lease.
func (s *Server) handoff(w http.ResponseWriter, r *http.Request) {
	var body handoffRequest
	if err := decodeTakeoverJSON(w, r, &body); err != nil || strings.TrimSpace(body.SessionPath) == "" || strings.TrimSpace(body.TargetWriterID) == "" {
		if err == nil {
			http.Error(w, "missing sessionPath or targetWriterId", http.StatusBadRequest)
		}
		return
	}
	if isSessionIDRoute(body.SessionPath) {
		s.handoffIdentity(w, r, strings.TrimSpace(body.SessionPath), strings.TrimSpace(body.TargetWriterID), body)
		return
	}
	mode := parseHandoffMode(body.Mode)
	realPath, err := s.resolveSessionPath(body.SessionPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if existing, ok := s.mirroredEntry(realPath); ok {
		if existing.targetWriterID != strings.TrimSpace(body.TargetWriterID) {
			http.Error(w, "session is already handed off to another writer", http.StatusConflict)
			return
		}
		writeJSON(w, existing.grant("already_handed_off"))
		return
	}
	if !body.Force && s.bc.Subscribers() > 0 {
		http.Error(w, "session is attached to a remote client; retry with force after confirming the takeover", http.StatusConflict)
		return
	}
	timeout := handoffTimeout(body.TimeoutMs)

	// Drain or cancel outside bindMu: waiting inside would freeze every other
	// command for up to the whole timeout.
	if err := s.quietSessionForHandoff(realPath, mode, timeout); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	s.bindMu.Lock()
	m, err := s.handoffLocked(realPath, strings.TrimSpace(body.TargetWriterID))
	s.bindMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), statusForHandoffError(err))
		return
	}
	writeJSON(w, m.grant("handed_off"))
}

// handoffIdentity releases a final-format identity the serve's foreground
// currently writes. The single-writer credential is the session directory's
// writer lock, so the release is: quiesce the foreground turn, flush, drop the
// foreground's binding without allocating a replacement identity, and
// synchronously close the handed-off runtime — its writer lock drops before the
// grant is answered.
func (s *Server) handoffIdentity(w http.ResponseWriter, r *http.Request, route, targetWriterID string, body handoffRequest) {
	ref, _, err := s.resolveSessionIdentity(route)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if existing, ok := s.mirroredEntry(route); ok {
		if existing.targetWriterID != targetWriterID {
			http.Error(w, "session is already handed off to another writer", http.StatusConflict)
			return
		}
		writeJSON(w, existing.grant("already_handed_off"))
		return
	}
	if !body.Force && s.bc.Subscribers() > 0 {
		http.Error(w, "session is attached to a remote client; retry with force after confirming the takeover", http.StatusConflict)
		return
	}
	mode := parseHandoffMode(body.Mode)
	timeout := handoffTimeout(body.TimeoutMs)
	// Drain or cancel outside bindMu, mirroring the legacy path.
	if err := s.quietIdentityForHandoff(ref, mode, timeout); err != nil {
		http.Error(w, err.Error(), statusForHandoffError(err))
		return
	}
	if handoffIdentityBeforeLockHookForTest != nil {
		handoffIdentityBeforeLockHookForTest()
	}

	s.bindMu.Lock()
	m, err := s.handoffIdentityLocked(r.Context(), route, ref, targetWriterID)
	s.bindMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), statusForHandoffError(err))
		return
	}
	writeJSON(w, m.grant("handed_off"))
}

// quietHandoffTarget answers the drain/cancel poll for one handoff target:
// held reports whether this serve currently runs the target, busy whether a
// turn is still active on it. Callers cancel toward idle in interrupt mode.
type quietHandoffTarget func() (ctrl control.SessionAPI, held, busy bool)

// quietHandoffLoop waits for (or cancels toward) an idle handoff target before
// the release transaction runs. It re-checks under bindMu afterwards: turn
// admission holds bindMu, so once the caller holds it and the target is idle,
// no new turn can start on it.
func quietHandoffLoop(target quietHandoffTarget, mode handoffMode, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		ctrl, held, busy := target()
		if !held {
			return errSessionNotHeld
		}
		if busy && mode == handoffModeInterrupt && ctrl != nil {
			ctrl.Cancel()
		}
		if !busy {
			return nil
		}
		if time.Now().After(deadline) {
			if mode == handoffModeInterrupt {
				return fmt.Errorf("session did not stop within %s; retry", timeout)
			}
			return fmt.Errorf("session is still running after %s; retry with mode=interrupt to cancel it", timeout)
		}
		time.Sleep(handoffPollInterval)
	}
}

// quietIdentityForHandoff waits for (or cancels toward) an idle holder of the
// identity — the foreground or a detached session — before the release
// transaction runs.
func (s *Server) quietIdentityForHandoff(ref session.SessionRef, mode handoffMode, timeout time.Duration) error {
	return quietHandoffLoop(func() (control.SessionAPI, bool, bool) {
		if cur, ok := s.ctl().(*control.Controller); ok {
			if current, bound := cur.SessionRef(); bound && current == ref {
				return cur, true, controllerHasActiveRuntimeWork(cur)
			}
		}
		if d := s.detachedIdentityHolder(ref); d != nil {
			return d.ctrl, true, controllerHasActiveRuntimeWork(d.ctrl)
		}
		return nil, false, false
	}, mode, timeout)
}

// handoffIdentityBeforeLockHookForTest runs between the unlocked quiet probe
// and the locked release so tests can admit a turn in that window.
var handoffIdentityBeforeLockHookForTest func()

// identityHolderLocked resolves which controller of this serve runs ref: the
// foreground, else the detached session bound to it. Callers hold bindMu.
func (s *Server) identityHolderLocked(ref session.SessionRef) (*control.Controller, *detachedSession) {
	if cur, ok := s.ctl().(*control.Controller); ok && cur.UsesExclusiveSession() {
		if current, bound := cur.SessionRef(); bound && current == ref {
			return cur, nil
		}
	}
	if d := s.detachedIdentityHolder(ref); d != nil {
		if concrete, ok := d.ctrl.(*control.Controller); ok {
			return concrete, d
		}
	}
	return nil, nil
}

// handoffIdentityLocked performs the identity release. Callers hold bindMu and
// have already quieted the holder; the busy state is re-checked here because
// turn admission also holds bindMu, so an idle holder observed under the lock
// cannot start a turn before the release completes.
func (s *Server) handoffIdentityLocked(ctx context.Context, route string, ref session.SessionRef, targetWriterID string) (mirroredSession, error) {
	holder, detached := s.identityHolderLocked(ref)
	if holder == nil {
		return mirroredSession{}, errSessionNotHeld
	}
	if controllerHasActiveRuntimeWork(holder) {
		return mirroredSession{}, errHandoffBusyAgain
	}
	service := holder.SessionService()
	if service == nil {
		return mirroredSession{}, errors.New("handoff: session service unavailable")
	}
	// The runtime can still be finalizing a turn the controller already reports
	// as done; Close would refuse it as busy, so treat it as busy up front.
	if live, ok := service.Runtime(ref); ok && live.Snapshot().Phase.Busy() {
		return mirroredSession{}, errHandoffBusyAgain
	}
	m, err := newMirroredSession(route, agent.SessionWriterID(), targetWriterID, mirrorPhasePending)
	if err != nil {
		return mirroredSession{}, fmt.Errorf("handoff: create generation: %w", err)
	}
	var taken *detachedSession
	if detached != nil {
		// Transfer ownership from the close-on-idle watcher before releasing,
		// exactly as the legacy detached handoff does; a retiring entry is
		// already closing and must not be handed off.
		taken = s.takeDetached(detached.path)
		if taken == nil {
			return mirroredSession{}, errHandoffBusyAgain
		}
		if controllerHasActiveRuntimeWork(holder) {
			_, _ = s.registerDetached(taken.ctrl, taken.keeper, taken.tag)
			return mirroredSession{}, errHandoffBusyAgain
		}
	}
	restore := func() {
		s.reattachAfterFailedHandoff(ctx, holder, ref)
		if taken != nil {
			_, _ = s.registerDetached(taken.ctrl, taken.keeper, taken.tag)
		}
	}
	// Release authority the way the legacy lease keeper does: flush and unbind,
	// allocating nothing. The holder is left never-bound, so the next turn or
	// /new allocates lazily and no empty canonical row is left in /sessions.
	if err := holder.ReleaseSessionForHandoff(); err != nil {
		restore()
		return mirroredSession{}, fmt.Errorf("handoff: release session binding: %w", err)
	}
	// Deterministic writer release: Close drops the writer lock now instead of
	// waiting out the idle-retirement TTL, so the taker's open cannot race a
	// lingering lease. A refused close re-attaches the holder to the live runtime.
	if err := service.Close(ctx, ref); err != nil {
		restore()
		if errors.Is(err, session.ErrRuntimeBusy) {
			return mirroredSession{}, errHandoffBusyAgain
		}
		return mirroredSession{}, fmt.Errorf("handoff: release session writer: %w", err)
	}
	if taken != nil {
		if taken.keeper != nil {
			taken.keeper.Release()
		}
		holder.Close()
		s.forgetSessionTag(holder)
	} else {
		// Re-point the frame tag: a stale tag stamps live frames with the
		// handed-off identity and the desktop pump drops them as background.
		s.setControllerPath(holder, "")
	}
	s.markMirrored(m)
	slog.Info("serve: final-format session handed off to local runtime", "session", route)
	s.bc.Emit(event.Event{
		Kind:        event.Notice,
		Level:       event.LevelWarn,
		Code:        event.NoticeCodeSessionTakenOver,
		Text:        "This session was taken over by a local Reasonix window and is read-only here.",
		Detail:      "A Reasonix window on this machine took over the conversation. It keeps streaming here; use \"take back\" to reclaim it.",
		SessionPath: route,
	})
	return m, nil
}

// reattachAfterFailedHandoff restores the foreground's binding to an identity
// whose release did not complete. The runtime is still the service's live
// instance (a refused close never retires it), so OpenSession re-binds and
// re-projects it; only when even that fails is the controller left unbound,
// which the next turn resolves by allocating lazily.
func (s *Server) reattachAfterFailedHandoff(ctx context.Context, concrete *control.Controller, ref session.SessionRef) {
	if cur, bound := concrete.SessionRef(); bound && cur == ref {
		return
	}
	if _, err := concrete.OpenSession(ctx, ref); err != nil {
		slog.Error("serve: re-attach identity after failed handoff", "session", ref.SessionID, "err", err)
	}
}

func parseHandoffMode(raw string) handoffMode {
	if handoffMode(raw) == handoffModeInterrupt {
		return handoffModeInterrupt
	}
	return handoffModeWait
}

func handoffTimeout(ms int) time.Duration {
	if ms <= 0 {
		return handoffDefaultTimeout
	}
	return time.Duration(ms) * time.Millisecond
}

func statusForHandoffError(err error) int {
	switch {
	case errors.Is(err, errSessionNotHeld), errors.Is(err, errHandoffBusyAgain):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

var (
	errSessionNotHeld   = errors.New("session is not held by this serve process")
	errHandoffBusyAgain = errors.New("session became busy again during handoff; retry")
)

// quietSessionForHandoff waits for (or cancels toward) an idle session before
// the binding transaction runs, covering both the foreground and a detached
// holder of the path.
func (s *Server) quietSessionForHandoff(realPath string, mode handoffMode, timeout time.Duration) error {
	return quietHandoffLoop(func() (control.SessionAPI, bool, bool) {
		cur := s.ctl()
		if cur != nil && agent.CanonicalSessionPath(cur.SessionPath()) == agent.CanonicalSessionPath(realPath) {
			return cur, true, controllerHasActiveRuntimeWork(cur)
		}
		if !s.detachedBusy(realPath) {
			return nil, false, false
		}
		s.detachedMu.Lock()
		d := s.detached[agent.CanonicalSessionPath(realPath)]
		ctrl := control.SessionAPI(nil)
		if d != nil {
			ctrl = d.ctrl
		}
		s.detachedMu.Unlock()
		return ctrl, ctrl != nil, ctrl != nil && controllerHasActiveRuntimeWork(ctrl)
	}, mode, timeout)
}

// handoffLocked performs the release transaction. Callers hold bindMu and
// have already quieted the session.
func (s *Server) handoffLocked(realPath, targetWriterID string) (mirroredSession, error) {
	cur := s.ctl()
	canonical := agent.CanonicalSessionPath(realPath)
	info, err := agent.LoadSessionLeaseInfo(realPath)
	if err != nil || info == nil || strings.TrimSpace(info.WriterID) == "" {
		return mirroredSession{}, fmt.Errorf("handoff: current lease identity unavailable")
	}
	m, err := newMirroredSession(canonical, info.WriterID, targetWriterID, mirrorPhasePending)
	if err != nil {
		return mirroredSession{}, fmt.Errorf("handoff: create generation: %w", err)
	}
	switch {
	case cur != nil && agent.CanonicalSessionPath(cur.SessionPath()) == canonical:
		if controllerHasActiveRuntimeWork(cur) {
			return mirroredSession{}, errHandoffBusyAgain
		}
		// Flush the transcript while this process still owns the file, then
		// hand the lease over. Rebind("") also unbinds write authority, so a
		// later save fails closed instead of racing the new writer.
		if err := cur.Snapshot(); err != nil {
			return mirroredSession{}, fmt.Errorf("handoff: snapshot session: %w", err)
		}
		if s.leases == nil {
			return mirroredSession{}, fmt.Errorf("handoff: lease keeper unavailable")
		}
		if err := s.leases.ReleaseForHandoff(targetWriterID, m.handoffID); err != nil {
			return mirroredSession{}, fmt.Errorf("handoff: release session lease: %w", err)
		}
	case s.detachedBusy(realPath):
		detached := s.takeDetached(realPath)
		if detached == nil {
			return mirroredSession{}, errHandoffBusyAgain
		}
		if controllerHasActiveRuntimeWork(detached.ctrl) {
			_, _ = s.registerDetached(detached.ctrl, detached.keeper, detached.tag)
			return mirroredSession{}, errHandoffBusyAgain
		}
		if err := detached.ctrl.Snapshot(); err != nil {
			_, _ = s.registerDetached(detached.ctrl, detached.keeper, detached.tag)
			return mirroredSession{}, fmt.Errorf("handoff: snapshot detached session: %w", err)
		}
		if detached.keeper == nil {
			_, _ = s.registerDetached(detached.ctrl, detached.keeper, detached.tag)
			return mirroredSession{}, fmt.Errorf("handoff: detached lease keeper unavailable")
		}
		if err := detached.keeper.ReleaseForHandoff(targetWriterID, m.handoffID); err != nil {
			_, _ = s.registerDetached(detached.ctrl, detached.keeper, detached.tag)
			return mirroredSession{}, fmt.Errorf("handoff: release detached session lease: %w", err)
		}
		detached.ctrl.Close()
		if concrete, ok := detached.ctrl.(*control.Controller); ok {
			s.forgetSessionTag(concrete)
		}
	default:
		return mirroredSession{}, errSessionNotHeld
	}
	s.markMirrored(m)
	slog.Info("serve: session handed off to local runtime", "session", canonical)
	s.bc.Emit(event.Event{
		Kind:        event.Notice,
		Level:       event.LevelWarn,
		Code:        event.NoticeCodeSessionTakenOver,
		Text:        "This session was taken over by a local Reasonix window and is read-only here.",
		Detail:      "A Reasonix window on this machine took over the conversation. It keeps streaming here; use \"take back\" to reclaim it.",
		SessionPath: canonical,
	})
	return m, nil
}
