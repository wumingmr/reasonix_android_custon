package acp

import (
	"context"
	"reasonix/internal/control"
)

func modelOnlyConfigDeltas(deltas []sessionConfigDelta) bool {
	if len(deltas) == 0 {
		return false
	}
	for _, d := range deltas {
		if d.axis != "model" {
			return false
		}
	}
	return true
}

func configBackgroundBlocked(ctrl acpController, modelOnly bool) bool {
	return !modelOnly || control.ModelReplacementBlocked(ctrl)
}

func (s *service) buildConfigReplacement(ctx context.Context, old acpController, params SessionParams, modelOnly bool) (*control.Controller, error) {
	finish, abort := func(*control.Controller) error { return nil }, func() {}
	if modelOnly {
		if c, ok := old.(*control.Controller); ok {
			params.SessionTemp, params.PersistentShell = c.SessionTemp(), c.PersistentShell()
		}
		var err error
		params.BackgroundScope, finish, abort, err = control.ReserveBackgroundReplacement(old)
		if err != nil {
			return nil, err
		}
	}
	defer abort()
	next, err := s.factory.NewSession(ctx, params)
	if err != nil {
		return nil, err
	}
	if err := finish(next); err != nil {
		next.ReleaseResources()
		return nil, err
	}
	return next, nil
}
