package sessioncatalog

import (
	"context"
	"errors"
	"os"
	"time"

	"reasonix/internal/historywork"
)

type metadataQueueJob struct {
	target          DirectoryTarget
	scan            *metadataScan
	ready           time.Time
	turn            uint64
	failures        int
	restartDeadline time.Time
}

// A known-invalid iterator must not consume an entire large root before the
// replacement can start. Coalesce a burst between slices, with bounded delay
// so a continuously changing root still gets discovery work admitted.
func deferMetadataRestart(job *metadataQueueJob, now time.Time) {
	if job.restartDeadline.IsZero() {
		job.restartDeadline = now.Add(time.Second)
	}
	job.ready = minTime(now.Add(historywork.PauseDuration), job.restartDeadline)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Keep one slot available for a newly visible workspace. Existing iterators
// are never evicted to admit another root: restarting them would repeatedly
// read the same prefix and could prevent large directories from completing.
const metadataIteratorLimit = 8

func selectMetadataQueueJob(jobs map[string]*metadataQueueJob, now time.Time, foreground bool, priority func(DirectoryTarget) bool) (string, *metadataQueueJob) {
	active := 0
	for _, job := range jobs {
		if job.scan != nil {
			active++
		}
	}
	var selected string
	var next *metadataQueueJob
	for key, job := range jobs {
		if job.ready.After(now) {
			continue
		}
		visible := priority(job.target)
		if !visible && foreground {
			continue
		}
		if job.scan == nil && (active >= metadataIteratorLimit || !visible && active >= metadataIteratorLimit-1) {
			continue
		}
		if next == nil || visible && !priority(next.target) || visible == priority(next.target) && (job.turn < next.turn || job.turn == next.turn && job.target.mutationSeq < next.target.mutationSeq) {
			selected, next = key, job
		}
	}
	return selected, next
}

// The queue retains a bounded set of directory iterators and admits only one
// slice. Admitted roots rotate without restarting their prefixes; waiting
// roots enter as earlier scans finish. Committed path updates use the
// independent small-metadata writer and never need an iterator slot.
func (c *Catalog) metadataReconcileLoop() {
	jobs := map[string]*metadataQueueJob{}
	defer func() {
		for _, job := range jobs {
			if job.scan != nil {
				job.scan.close(c.workerCtx, context.Canceled)
			}
		}
	}()
	var turn uint64
	for c.workerCtx.Err() == nil {
		c.reconcileDirtyMu.Lock()
		c.reconcileQueued.Range(func(key, value any) bool {
			id := key.(string)
			if jobs[id] == nil {
				jobs[id] = &metadataQueueJob{target: value.(DirectoryTarget)}
				delete(c.reconcileDirty, id)
			}
			return true
		})
		c.reconcileDirtyMu.Unlock()
		now := c.opts.Now()
		foreground := c.opts.Maintenance != nil && c.opts.Maintenance.ForegroundActive()
		selected, next := selectMetadataQueueJob(jobs, now, foreground, c.isPriorityDirectory)
		if next == nil {
			timer := time.NewTimer(historywork.PauseDuration)
			select {
			case <-c.stop:
				timer.Stop()
				return
			case <-c.reconcileCh:
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		turn++
		next.turn = turn
		c.reconcileDirtyMu.Lock()
		latest, invalidated := c.reconcileDirty[selected]
		if invalidated && (next.scan != nil || !next.restartDeadline.IsZero()) {
			delete(c.reconcileDirty, selected)
		} else {
			invalidated = false
		}
		c.reconcileDirtyMu.Unlock()
		if invalidated {
			if next.scan != nil {
				// Abandoning an incomplete observation cannot confirm missing
				// rows. Keep its journal and visible prefix until the new EOF.
				c.observeDiscovery(next.target, "superseded", "", "")
				next.scan.close(c.workerCtx, nil)
				next.scan = nil
			}
			next.target = newestReconcileTarget(next.target, latest)
			deferMetadataRestart(next, now)
			if next.ready.After(now) {
				continue
			}
		}
		var err error
		if next.scan == nil {
			// Waiting for an iterator or priority slot is not a running scan.
			// Fold all pre-dispatch invalidations into this scan; only changes
			// arriving after dispatch need a follow-up pass.
			target, owned := c.resolveReconcileToken(next.target)
			if !owned {
				delete(jobs, selected)
				continue
			}
			next.target = target
			next.restartDeadline = time.Time{}
			c.observeDiscovery(next.target, "started", "", "")
			if c.testReconcileStartHook != nil {
				c.testReconcileStartHook(next.target)
			}
			next.scan, err = c.startMetadataScan(c.workerCtx, next.target, next.target.mutationSeq, true)
		}
		var done bool
		var bytes int64
		if err == nil {
			done, bytes, err = next.scan.step(c.workerCtx)
		}
		if errors.Is(err, errMetadataScanBusy) || errors.Is(err, historywork.ErrForegroundActive) {
			next.ready = now.Add(historywork.PauseDuration)
			continue
		}
		if done || err != nil {
			c.finishMetadataQueueJob(jobs, selected, next, done, err)
		}
		// Apply a global pause, including when the next slice belongs to a
		// different root. Per-root timers alone multiply the allowed I/O rate.
		if c.opts.Maintenance == nil {
			if err := historywork.Pause(c.workerCtx, bytes); err != nil {
				return
			}
		}
	}
}

func (c *Catalog) PrioritizeWorkspace(scope, root string) {
	scope, root = normalizeScope(scope, root)
	c.priorityWorkspace.Store(scope + "\x00" + c.workspaceRootKey(scope, root))
}

func (c *Catalog) isPriorityDirectory(target DirectoryTarget) bool {
	scope, root := normalizeScope(target.Scope, target.WorkspaceRoot)
	key, _ := c.priorityWorkspace.Load().(string)
	return key == scope+"\x00"+c.workspaceRootKey(scope, root)
}

func (c *Catalog) finishMetadataQueueJob(jobs map[string]*metadataQueueJob, selected string, next *metadataQueueJob, done bool, err error) {
	phase, failure := "completed", ""
	if err != nil {
		phase, failure = "failed", "io_or_database"
		if errors.Is(err, context.Canceled) {
			failure = "canceled"
		} else if os.IsPermission(err) {
			failure = "permission"
		} else if os.IsNotExist(err) {
			failure = "missing"
		}
	}
	c.observeDiscovery(next.target, phase, "", failure)
	c.observeDatabaseError(err)
	if next.scan != nil {
		next.scan.close(c.workerCtx, err)
		next.scan = nil
	}
	if done && err == nil {
		c.settleReconcileTarget(next.target)
	}
	c.reconcileDirtyMu.Lock()
	if follow, dirty := c.reconcileDirty[selected]; dirty {
		delete(c.reconcileDirty, selected)
		next.target, next.failures, next.ready = follow, 0, time.Time{}
	} else if err != nil && !errors.Is(err, context.Canceled) && !os.IsNotExist(err) && !os.IsPermission(err) && next.failures < 3 {
		next.ready = c.opts.Now().Add([]time.Duration{time.Second, 5 * time.Second, 30 * time.Second}[next.failures])
		next.failures++
	} else {
		c.reconcileQueued.Delete(selected)
		delete(jobs, selected)
		if done := c.reconcileDone[selected]; done != nil {
			delete(c.reconcileDone, selected)
			close(done)
		}
	}
	c.reconcileDirtyMu.Unlock()
}
