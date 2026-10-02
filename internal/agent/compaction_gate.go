package agent

import (
	"context"
	"sync"
)

// compactionGate is a zero-value, cancellation-aware single-flight latch.
type compactionGate struct {
	once  sync.Once
	token chan struct{}
}

func (g *compactionGate) acquire(ctx context.Context) error {
	g.once.Do(func() { g.token = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case g.token <- struct{}{}:
		return g.acquired(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *compactionGate) acquired(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		g.Unlock()
		return err
	}
	return nil
}

func (g *compactionGate) Lock()   { _ = g.acquire(context.Background()) }
func (g *compactionGate) Unlock() { <-g.token }
