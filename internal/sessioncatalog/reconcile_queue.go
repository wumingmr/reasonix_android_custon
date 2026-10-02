package sessioncatalog

import (
	"context"
	"runtime"
	"strings"
	"time"
)

// RequestReconcile makes the channel a wake signal while the maps retain the
// newest target. Session saves never wait for catalog work.
func (c *Catalog) RequestReconcile(target DirectoryTarget) bool {
	_, accepted := c.ScheduleReconcile(target)
	return accepted
}

// ScheduleReconcile joins the catalog's single discovery owner. The returned
// signal settles after all coalesced source changes have been visited; callers
// must also select their own cancellation when waiting during shutdown.
func (c *Catalog) ScheduleReconcile(target DirectoryTarget) (<-chan struct{}, bool) {
	if c == nil || strings.TrimSpace(target.Path) == "" {
		return nil, false
	}
	target.Path = cleanCatalogAccessPath(target.Path)
	key := queuePathKey(target.Path)
	if key == "" {
		return nil, false
	}
	target.mutationSeq = c.mutationSeq.Add(1)
	if c.opts.OnDiscovery != nil {
		// Keep the function symbol, never runtime file names or stack arguments.
		// Skip only the two public queue wrappers to identify the real owner.
		var pcs [8]uintptr
		n := runtime.Callers(2, pcs[:])
		frames := runtime.CallersFrames(pcs[:n])
		for {
			frame, more := frames.Next()
			if !strings.HasSuffix(frame.Function, ".(*Catalog).RequestReconcile") && !strings.HasSuffix(frame.Function, ".(*Catalog).RequestIndexSession") {
				c.observeDiscovery(target, "requested", frame.Function, "")
				break
			}
			if !more {
				break
			}
		}
	}
	if c.opts.MetadataOnly {
		// A saturated path queue has already committed its authoritative save.
		// Persist the root invalidation before acknowledging maintenance so a
		// crash cannot turn an overflow into a permanently stale ready catalog.
		if err := c.persistReconcileTarget(target); err != nil {
			return nil, false
		}
	}
	c.reconcileDirtyMu.Lock()
	defer c.reconcileDirtyMu.Unlock()
	select {
	case <-c.stop:
		return nil, false
	default:
	}
	if c.reconcileDone == nil {
		c.reconcileDone = map[string]chan struct{}{}
	}
	done := c.reconcileDone[key]
	if done == nil {
		done = make(chan struct{})
		c.reconcileDone[key] = done
	}
	if queued, loaded := c.reconcileQueued.Load(key); loaded {
		target = newestReconcileTarget(queued.(DirectoryTarget), target)
		c.reconcileDirty[key] = target
		c.reconcileQueued.Store(key, target)
		return done, true
	}
	c.reconcileQueued.Store(key, target)
	select {
	case c.reconcileCh <- target:
		return done, true
	default:
		c.reconcileDirty[key] = target
		return done, true
	}
}

func (c *Catalog) markReconcileDirty(target DirectoryTarget) {
	key := queuePathKey(target.Path)
	c.reconcileDirtyMu.Lock()
	if queued, ok := c.reconcileQueued.Load(key); ok {
		target = newestReconcileTarget(queued.(DirectoryTarget), target)
	}
	if dirty, ok := c.reconcileDirty[key]; ok {
		target = newestReconcileTarget(dirty, target)
	}
	c.reconcileDirty[key] = target
	c.reconcileQueued.Store(key, target)
	c.reconcileDirtyMu.Unlock()
}

func (c *Catalog) resolveReconcileToken(target DirectoryTarget) (DirectoryTarget, bool) {
	key := queuePathKey(target.Path)
	c.reconcileDirtyMu.Lock()
	defer c.reconcileDirtyMu.Unlock()
	queued, owned := c.reconcileQueued.Load(key)
	if !owned {
		return DirectoryTarget{}, false
	}
	target = newestReconcileTarget(target, queued.(DirectoryTarget))
	if latest, dirty := c.reconcileDirty[key]; dirty {
		target = newestReconcileTarget(target, latest)
		delete(c.reconcileDirty, key)
	}
	c.reconcileQueued.Store(key, target)
	return target, true
}

func newestReconcileTarget(current, candidate DirectoryTarget) DirectoryTarget {
	if candidate.mutationSeq > current.mutationSeq {
		return candidate
	}
	return current
}

func (c *Catalog) takeReconcileDirty() (DirectoryTarget, bool) {
	c.reconcileDirtyMu.Lock()
	defer c.reconcileDirtyMu.Unlock()
	for key, target := range c.reconcileDirty {
		delete(c.reconcileDirty, key)
		c.reconcileQueued.Store(key, target)
		return target, true
	}
	return DirectoryTarget{}, false
}

func (c *Catalog) reconcileLoop() {
	defer c.workers.Done()
	select {
	case <-c.discoveryStart:
	case <-c.stop:
		return
	}
	if c.opts.MetadataOnly {
		c.metadataReconcileLoop()
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case token := <-c.reconcileCh:
			if target, ok := c.resolveReconcileToken(token); ok {
				c.runQueuedReconcile(target)
			}
			continue
		default:
		}
		if target, ok := c.takeReconcileDirty(); ok {
			c.runQueuedReconcile(target)
			continue
		}
		select {
		case token := <-c.reconcileCh:
			if target, ok := c.resolveReconcileToken(token); ok {
				c.runQueuedReconcile(target)
			}
		case <-ticker.C:
		case <-c.stop:
			return
		}
	}
}

// ResumeDiscovery separates a queryable projection from background discovery.
// Initial watched roots must join restored journal entries before dispatch;
// admitting them after resume would turn the same startup discovery into a
// second scan whenever an interrupted root was already pending on disk.
// Rejected roots remain the caller's responsibility until admission succeeds.
func (c *Catalog) ResumeDiscovery(initial ...DirectoryTarget) (rejected []DirectoryTarget) {
	for _, target := range initial {
		if !c.RequestReconcile(target) {
			rejected = append(rejected, target)
		}
	}
	c.discoveryOnce.Do(func() { close(c.discoveryStart) })
	return rejected
}

func (c *Catalog) runQueuedReconcile(target DirectoryTarget) {
	key := queuePathKey(target.Path)
	for {
		if c.testReconcileStartHook != nil {
			c.testReconcileStartHook(target)
		}
		// A metadata scan is resumable between slices. A directory-size timeout
		// would restart large directories forever before reaching EOF.
		ctx, cancel := context.WithCancel(c.workerCtx)
		if !c.opts.MetadataOnly {
			cancel()
			ctx, cancel = context.WithTimeout(c.workerCtx, 2*time.Minute)
		}
		_ = c.reconcileDirectory(ctx, target, target.mutationSeq)
		cancel()

		c.reconcileDirtyMu.Lock()
		followUp, dirty := c.reconcileDirty[key]
		if dirty {
			delete(c.reconcileDirty, key)
			c.reconcileQueued.Store(key, followUp)
			c.reconcileDirtyMu.Unlock()
			target = followUp
			continue
		}
		c.reconcileQueued.Delete(key)
		if done := c.reconcileDone[key]; done != nil {
			delete(c.reconcileDone, key)
			close(done)
		}
		c.reconcileDirtyMu.Unlock()
		return
	}
}
