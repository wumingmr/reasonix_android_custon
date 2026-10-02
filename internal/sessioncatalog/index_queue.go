package sessioncatalog

import (
	"context"
	"path/filepath"
	"time"
)

// RequestIndexSession coalesces an authoritative session write by path. A full
// queue journals a root invalidation before returning; notification consumers
// must use TryRequestIndexSession and coalesce overflow at their own boundary.
func (c *Catalog) RequestIndexSession(target DirectoryTarget, path string) bool {
	if c == nil || cleanCatalogAccessPath(path) == "" {
		return false
	}
	if c.TryRequestIndexSession(target, path) {
		return true
	}
	if target.Path == "" {
		target.Path = filepath.Dir(cleanCatalogAccessPath(path))
	}
	// Losing a wake-up must not lose the authoritative write. The root
	// queue coalesces overflow and retains it until reconciliation.
	c.RequestReconcile(target)
	return false
}

// TryRequestIndexSession never waits for filesystem or database work. On false
// the caller owns the root invalidation, including retrying failed journal
// admission. This lets filesystem consumers drain bursts without doing one
// synchronous journal transaction for every overflowing file notification.
func (c *Catalog) TryRequestIndexSession(target DirectoryTarget, path string) bool {
	if c == nil {
		return false
	}
	path = cleanCatalogAccessPath(path)
	if path == "" {
		return false
	}
	target.Path = cleanCatalogAccessPath(target.Path)
	if target.Path == "" {
		target.Path = filepath.Dir(path)
	}
	key := queuePathKey(path)
	request := sessionPathRequest{target: target, path: path, queueKey: key, sequence: c.mutationSeq.Add(1)}
	c.pathQueueMu.Lock()
	if _, loaded := c.pathQueued.Load(key); loaded {
		c.pathQueued.Store(key, request)
		c.pathQueueMu.Unlock()
		return true
	}
	c.pathQueued.Store(key, request)
	select {
	case c.pathCh <- request:
		c.pathQueueMu.Unlock()
		return true
	case <-c.stop:
		c.pathQueued.Delete(key)
		c.pathQueueMu.Unlock()
		return false
	default:
		c.pathQueued.Delete(key)
		c.pathQueueMu.Unlock()
		return false
	}
}

func (c *Catalog) sessionPathLoop() {
	defer c.workers.Done()
	for {
		select {
		case token := <-c.pathCh:
			c.pathQueueMu.Lock()
			queued, ok := c.pathQueued.LoadAndDelete(token.queueKey)
			c.pathQueueMu.Unlock()
			if !ok {
				continue
			}
			request := queued.(sessionPathRequest)
			ctx, cancel := context.WithTimeout(c.workerCtx, 30*time.Second)
			_ = c.indexSessionPath(ctx, request.target, request.path, request.sequence)
			cancel()
		case <-c.stop:
			return
		}
	}
}
