package serve

import (
	"context"
	"reasonix/internal/boot"
	"reasonix/internal/control"
)

func (s *Server) buildModelCandidate(ctx context.Context, ref string, opts boot.Options, inherit bool) (*control.Controller, error) {
	finish, abort := func(*control.Controller) error { return nil }, func() {}
	if inherit {
		var err error
		opts.BackgroundScope, finish, abort, err = control.ReserveBackgroundReplacement(s.ctl())
		if err != nil {
			return nil, err
		}
	}
	defer abort()
	var c *control.Controller
	var err error
	switch {
	case s.buildControllerWithOptions != nil:
		c, err = s.buildControllerWithOptions(ctx, ref, opts)
	case s.buildController != nil:
		c, err = s.buildController(ctx, ref)
	default:
		c, err = boot.Build(ctx, opts)
	}
	if err != nil {
		return nil, err
	}
	if err := finish(c); err != nil {
		c.ReleaseResources()
		return nil, err
	}
	return c, nil
}
