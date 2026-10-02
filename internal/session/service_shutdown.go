package session

import (
	"context"
	"errors"
)

// CloseAll releases every runtime this service still owns. A runtime a client
// is still bound to is reported, not torn down underneath that client.
func (s *Service) CloseAll(ctx context.Context) error {
	return s.closeAll(ctx, false)
}

func (s *Service) closeAll(ctx context.Context, terminal bool) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	runtimes := make([]*Runtime, 0, len(s.active))
	for _, runtime := range s.active {
		runtimes = append(runtimes, runtime)
	}
	s.mu.Unlock()
	var closeErr error
	for _, runtime := range runtimes {
		closeErr = errors.Join(closeErr, s.closeRuntime(ctx, runtime, "", terminal))
	}
	return closeErr
}

// Shutdown closes execution ownership and the query projection workers that
// share this service's persistence root. CloseAll intentionally remains the
// runtime-only primitive; hosts and short-lived import services must use this
// lifecycle boundary before releasing or removing the root directory.
//
// Because callers rely on that guarantee, Shutdown is terminal: a runtime a
// client never unbound is still closed, so its writer lease and recovery
// handles are released instead of surviving until process exit. The leaked
// binding is reported rather than swallowed.
func (s *Service) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	// Stop and join cold-query workers before closing live runtimes. Otherwise
	// a worker can open a recovery projection after CloseAll collected its
	// runtime set and leave the Bolt handle behind during Windows cleanup.
	s.query.Close()
	return s.closeAll(ctx, true)
}
