package main

import (
	"sync"
	"time"
)

type runtimeProjectionEvents struct {
	mu      sync.Mutex
	pending map[*WorkspaceTab]localRuntimeUpdate
	done    chan struct{}
	flush   chan struct{}
}

// The per-controller stream remains immediate. Only the rebuildable global
// sidebar projection is batched, with one publisher for concurrent producers.
func (a *App) queueRuntimeProjection(update localRuntimeUpdate) {
	c := &a.runtimeStateProjection.events
	c.mu.Lock()
	if a.shuttingDown.Load() {
		c.mu.Unlock()
		return
	}
	if c.pending == nil {
		c.pending = map[*WorkspaceTab]localRuntimeUpdate{}
	}
	previous, exists := c.pending[update.tab]
	if !exists || previous.state.RuntimeEpoch != update.state.RuntimeEpoch || previous.state.Revision <= update.state.Revision {
		c.pending[update.tab] = update
	}
	if c.done != nil {
		c.mu.Unlock()
		return
	}
	c.done, c.flush = make(chan struct{}), make(chan struct{}, 1)
	flush := c.flush
	c.mu.Unlock()
	go func() {
		for {
			timer := time.NewTimer(16 * time.Millisecond)
			select {
			case <-timer.C:
			case <-flush:
			}
			timer.Stop()
			c.mu.Lock()
			updates := c.pending
			c.pending = nil
			c.mu.Unlock()
			bindings := a.sampleLocalRuntimeBindingsWithUpdates(updates, true)
			a.emitRuntimeProjection(a.projectRuntimeBindings(bindings), true)
			c.mu.Lock()
			if len(c.pending) == 0 {
				close(c.done)
				c.done, c.flush = nil, nil
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
		}
	}()
}

func (a *App) flushRuntimeProjections() {
	c := &a.runtimeStateProjection.events
	c.mu.Lock()
	done := c.done
	if done != nil {
		select {
		case c.flush <- struct{}{}:
		default:
		}
	}
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}
