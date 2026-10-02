package boot

import (
	"errors"
	"reasonix/internal/control"
)

func (opts *Options) inheritSessionBinding(old *control.Controller) {
	opts.NativeLegacySession = old.NativeLegacySession() || (!old.UsesExclusiveSession() && old.SessionPath() != "")
	opts.SessionCreateService = old.SessionCreationService()
	if opts.NativeLegacySession && old.SessionService() != nil {
		opts.SessionService = old.SessionService()
	}
	if service, runtime, ok := old.SessionBinding(); ok {
		opts.SessionService = service
		opts.SessionRuntime = runtime
		opts.SessionHostID = runtime.Ref().HostID
	}
}

func (opts Options) validateSessionBinding() error {
	if opts.NativeLegacySession && opts.SessionRuntime != nil {
		return errors.New("native legacy session cannot bind a canonical runtime")
	}
	if opts.SessionRuntime != nil && opts.SessionService == nil {
		return errors.New("v3 session runtime requires a session service")
	}
	return nil
}
