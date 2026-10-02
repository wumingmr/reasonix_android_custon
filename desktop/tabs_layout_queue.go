package main

import (
	"sync"
	"time"
)

type tabLayoutSave struct {
	dir      string
	entries  []desktopTabEntry
	activeID string
	version  uint64
}

// Only rebuildable navigation layout is coalesced. Session durability barriers
// remain synchronous in SetActiveTab and are never owned by this queue.
type tabLayoutWriter struct {
	mu      sync.Mutex
	pending *tabLayoutSave
	done    chan struct{}
	flush   chan struct{}
}

func (a *App) queueTabLayoutSave(dir string, entries []desktopTabEntry, activeID string, version uint64) {
	q := &a.desktopSessions.layoutWrites
	q.mu.Lock()
	if a.shuttingDown.Load() {
		q.mu.Unlock()
		return
	}
	if q.pending == nil || version >= q.pending.version {
		q.pending = &tabLayoutSave{dir, entries, activeID, version}
	}
	if q.done != nil {
		q.mu.Unlock()
		return
	}
	q.done, q.flush = make(chan struct{}), make(chan struct{}, 1)
	flush := q.flush
	q.mu.Unlock()
	go func() {
		timer := time.NewTimer(25 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-flush:
		}
		for {
			q.mu.Lock()
			next := q.pending
			q.pending = nil
			if next == nil {
				close(q.done)
				q.done, q.flush = nil, nil
				q.mu.Unlock()
				return
			}
			q.mu.Unlock()
			a.saveTabsWrite(next.dir, next.entries, next.activeID, next.version)
		}
	}()
}

func (a *App) queueCurrentTabLayout() {
	a.mu.Lock()
	dir, entries, activeID, version := a.saveTabsCollectLocked()
	a.mu.Unlock()
	a.queueTabLayoutSave(dir, entries, activeID, version)
}

func (a *App) flushTabLayoutWrites() {
	q := &a.desktopSessions.layoutWrites
	q.mu.Lock()
	done := q.done
	if done != nil {
		select {
		case q.flush <- struct{}{}:
		default:
		}
	}
	q.mu.Unlock()
	if done != nil {
		<-done
	}
}
