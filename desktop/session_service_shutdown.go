package main

import (
	"context"
	"errors"
	"log/slog"

	"reasonix/internal/session"
)

// Run after controllers and lifecycle barriers release their bindings.
func (a *App) closeSessionServices() {
	if err := a.closeSessionServicesResult(); err != nil {
		slog.Warn("desktop: close session service", "err", err)
	}
}

func (a *App) closeSessionServicesResult() error {
	a.closeHistoryReaders()
	a.desktopSessions.readSnapshots.close()
	a.sessionServicesMu.Lock()
	services := make([]*session.Service, 0, len(a.sessionServices))
	for _, service := range a.sessionServices {
		services = append(services, service)
	}
	for _, service := range a.historicalSessionServices {
		services = append(services, service)
	}
	a.sessionServicesMu.Unlock()
	var failures error
	for _, service := range services {
		if err := service.Shutdown(context.Background()); err != nil {
			if onlyReleasedBindingErrors(err) {
				slog.Warn("desktop: released leaked session bindings during shutdown", "err", err)
			} else {
				failures = errors.Join(failures, err)
			}
		}
	}
	return failures
}

// Shutdown may release an idle leaked binding successfully. Do not turn that
// warning into an unresolvable exit failure, or swallow errors joined with it.
func onlyReleasedBindingErrors(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if !onlyReleasedBindingErrors(child) {
				return false
			}
		}
		return true
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return onlyReleasedBindingErrors(wrapped)
	}
	return errors.Is(err, session.ErrRuntimeBound)
}
