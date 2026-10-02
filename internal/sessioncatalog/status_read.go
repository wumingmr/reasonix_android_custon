package sessioncatalog

import (
	"context"
)

func (c *Catalog) loadStatus(ctx context.Context) error {
	var revision uint64
	if err := c.readDB(ctx).QueryRowContext(ctx, `SELECT revision FROM catalog_state WHERE id=1`).Scan(&revision); err != nil {
		return err
	}
	c.revision.Store(revision)
	c.statusMu.Lock()
	c.status.Revision = revision
	c.statusMu.Unlock()
	c.refreshCounts(ctx)
	return nil
}

func (c *Catalog) Status() Status {
	if c == nil {
		return Status{State: StateDegraded, Mode: ModeMemory, LastError: "session catalog unavailable"}
	}
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	status := c.status
	if c.readable() != nil && status.State != StateClosed {
		status.State, status.LastError = StateDegraded, c.invalidReason.Error()
	}
	return status
}
