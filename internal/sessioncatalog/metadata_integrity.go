package sessioncatalog

import (
	"context"
	"errors"
	"time"

	"reasonix/internal/projectiondb"
)

var ErrCatalogInvalidated = errors.New("catalog generation invalidated")

// Invalidated closes only when corruption is proven. The Desktop owner closes
// this generation and reopens with synchronous validation/quarantine enabled.
func (c *Catalog) Invalidated() <-chan struct{} { return c.invalidated }

func (c *Catalog) readable() error {
	select {
	case <-c.invalidated:
		return ErrCatalogInvalidated
	default:
		return nil
	}
}

func (c *Catalog) observeDatabaseError(err error) {
	if !c.opts.MetadataOnly || !projectiondb.IsCorruptionError(err) {
		return
	}
	c.invalidateOnce.Do(func() {
		c.invalidReason = err
		c.statusMu.Lock()
		c.status.State, c.status.LastError = StateDegraded, err.Error()
		c.statusMu.Unlock()
		if c.workerCancel != nil {
			c.workerCancel()
		}
		close(c.invalidated)
	})
}

func (c *Catalog) verifyMetadataIntegrity() {
	defer c.workers.Done()
	defer close(c.integrityDone)
	select {
	case <-c.workerCtx.Done():
		return
	case <-c.discoveryStart:
	}
	wait := c.opts.waitMetadataRetry
	if wait == nil {
		wait = waitMetadataAuditRetry
	}
	backoff := []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}
	lastError := ""
	for attempt := 0; ; attempt++ {
		err := c.checkMetadataIntegrity()
		if c.workerCtx.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		if err == nil {
			c.statusMu.Lock()
			if lastError != "" && c.status.LastError == lastError {
				c.status.State, c.status.LastError = StateReady, ""
			}
			c.statusMu.Unlock()
			return
		}
		lastError = err.Error()
		c.statusMu.Lock()
		c.status.State, c.status.LastError = StateDegraded, lastError
		c.statusMu.Unlock()
		c.observeDatabaseError(err)
		if c.readable() != nil || attempt == len(backoff) {
			return
		}
		if err := wait(c.workerCtx, backoff[attempt]); err != nil {
			return
		}
	}
}

func (c *Catalog) checkMetadataIntegrity() error {
	if c.opts.Maintenance != nil {
		release, err := c.opts.Maintenance.Background(c.workerCtx)
		if err != nil {
			return err
		}
		defer release()
	}
	verify := c.opts.verifyMetadata
	if verify == nil {
		verify = func(ctx context.Context) error { return projectiondb.CheckIntegrity(ctx, c.db) }
	}
	return verify(c.workerCtx)
}

func waitMetadataAuditRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Catalog) registerReadLease(lease *ReadLease) bool {
	c.readLeasesMu.Lock()
	defer c.readLeasesMu.Unlock()
	if c.readLeasesClosed || c.workerCtx.Err() != nil || c.readable() != nil {
		return false
	}
	c.readLeases[lease] = struct{}{}
	return true
}

func (c *Catalog) closeReadLeases() {
	c.readLeasesMu.Lock()
	c.readLeasesClosed = true
	leases := make([]*ReadLease, 0, len(c.readLeases))
	for lease := range c.readLeases {
		leases = append(leases, lease)
	}
	c.readLeasesMu.Unlock()
	for _, lease := range leases {
		lease.Close()
	}
}
