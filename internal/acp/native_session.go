package acp

import (
	"context"
	"errors"
	"os"

	"reasonix/internal/control"
	"reasonix/internal/session"
)

func existingTranscript(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func canonicalSessionExists(ctx context.Context, ctrl *control.Controller, id string) (bool, error) {
	service := ctrl.SessionService()
	if service == nil {
		return ctrl.UsesExclusiveSession(), nil
	}
	_, err := service.Query().Stat(ctx, session.SessionRef{HostID: service.HostID(), SessionID: id})
	if errors.Is(err, session.ErrSessionNotFound) || os.IsNotExist(err) {
		return ctrl.UsesExclusiveSession(), nil
	}
	return err == nil, err
}

func openCanonicalSession(ctx context.Context, ctrl *control.Controller, id, method string) error {
	service := ctrl.SessionService()
	if service == nil {
		return &RPCError{Code: ErrInternal, Message: method + ": v3 session service is unavailable"}
	}
	if _, err := ctrl.OpenSession(ctx, session.SessionRef{HostID: service.HostID(), SessionID: id}); err != nil {
		return &RPCError{Code: ErrInvalidParams, Message: method + ": unknown session " + id}
	}
	return nil
}

func (s *service) bindSessionClients(id string, params *SessionParams) {
	s.bindSessionPathHandlers(id, params)
	s.bindClientIO(params, id)
}
