package serve

import (
	"context"
	"log/slog"
	"reasonix/internal/control"
	"reasonix/internal/session"
)

// sessionAPIRef reads the immutable v3 identity off any session API without
// requiring the concrete controller type.
func sessionAPIRef(ctrl control.SessionAPI) (session.SessionRef, bool) {
	identity, ok := ctrl.(interface {
		SessionRef() (session.SessionRef, bool)
	})
	if !ok {
		return session.SessionRef{}, false
	}
	return identity.SessionRef()
}

// busySwitchIdentity backgrounds a busy exclusive-session controller and
// brings the target identity session to the foreground — the identity-route
// counterpart of busyDetach. The replacement controller opens the target
// through the shared session service; the detached registry (identity-keyed)
// keeps the running turn observable until it is re-attached. It fails with
// errIdentityServiceUnavailable when this host cannot build an
// identity-capable replacement; the caller then keeps the historical refusal.
func (s *Server) busySwitchIdentity(ctx context.Context, cur *control.Controller, ref session.SessionRef) error {
	if s.tagFor(cur) == nil {
		return errSessionTagUnavailable
	}
	next, tag, err := s.buildTaggedMode(ctx, currentModelRef(cur), false, false)
	if err != nil {
		return err
	}
	if next.SessionService() == nil {
		s.closeTaggedController(next)
		return errIdentityServiceUnavailable
	}
	if _, err := next.OpenSession(ctx, ref); err != nil {
		s.closeTaggedController(next)
		return err
	}
	if bound, ok := next.SessionRef(); ok {
		tag.PrimeIdentity("", bound.SessionID)
	}
	next.EnableInteractiveApproval()
	next.SetOnSessionRecovered(s.sessionRecoveryHandler(next, s.leases))
	if !s.publishControllerSwap(cur, next, "") {
		s.closeTaggedController(next)
		return errReplacedDuringBind
	}
	var demoted *control.SessionLeaseKeeper
	if s.leases != nil {
		demoted = s.leases.Split()
	}
	if _, err := s.registerDetached(cur, demoted, nil); err != nil {
		// bindMu prevents another foreground swap here. Roll publication back so
		// a registry failure cannot strand a running controller.
		_ = s.publishControllerSwap(next, cur, cur.SessionPath())
		s.closeTaggedController(next)
		if demoted != nil {
			s.leases.Adopt(demoted)
		}
		return err
	}
	tag.Activate()
	slog.Info("serve: busy identity session detached", "session", ref.SessionID)
	return nil
}
