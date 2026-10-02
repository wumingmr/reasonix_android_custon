package session

import (
	"context"
	"sync"

	"reasonix/internal/historywork"
)

type queryReadScope struct {
	ctx    context.Context
	cancel context.CancelFunc
	refs   int
}

type queryReadScopeKey struct{}

// ConfigureHistoryMaintenance is called before publishing a Desktop service.
// All roots share the same preparation budget; passive lists never replay logs.
func (s *Service) ConfigureHistoryMaintenance(coordinator *historywork.Coordinator) {
	s.query.maintenance = coordinator
	s.query.metadataOnlyListings = true
}

// AcquireHistoryReader owns only query preparation, never runtime recovery or
// writer authority. Releasing the last reader cancels its rebuilds. A new
// reader receives a new context even if the old build is still unwinding.
func (q *Query) AcquireHistoryReader(ref SessionRef) (context.Context, func(), error) {
	if err := ref.validate(q.hostID); err != nil {
		return nil, nil, err
	}
	q.readMu.Lock()
	if q.readScopes == nil {
		q.readScopes = make(map[string]*queryReadScope)
	}
	scope := q.readScopes[ref.SessionID]
	if scope == nil {
		ctx, cancel := context.WithCancel(q.rebuildCtx)
		scope = &queryReadScope{ctx: ctx, cancel: cancel}
		q.readScopes[ref.SessionID] = scope
	}
	scope.refs++
	q.readMu.Unlock()
	var once sync.Once
	return context.WithValue(scope.ctx, queryReadScopeKey{}, scope), func() {
		once.Do(func() {
			q.readMu.Lock()
			scope.refs--
			if scope.refs == 0 {
				scope.cancel()
				if q.readScopes[ref.SessionID] == scope {
					delete(q.readScopes, ref.SessionID)
				}
			}
			q.readMu.Unlock()
		})
	}, nil
}

func (q *Query) historyReadContext(_ string, callers ...context.Context) context.Context {
	if len(callers) > 0 {
		if scope, ok := callers[0].Value(queryReadScopeKey{}).(*queryReadScope); ok {
			return scope.ctx
		}
	}
	// A released binding must not revive a job under the service lifetime
	// between its cancellation check and acquisition of readMu.
	if len(callers) > 0 && callers[0].Err() != nil {
		return callers[0]
	}
	// Legacy "preparing" responses end the request before its worker finishes.
	// They cannot borrow another client's binding; new RPCs use the explicit
	// cancellable read bindings above instead of this service lifetime.
	return q.rebuildCtx
}

func (q *Query) acquireHistoryPreparation(ctx context.Context) (func(), error) {
	if q.maintenance == nil {
		return func() {}, ctx.Err()
	}
	return q.maintenance.Foreground(ctx)
}
