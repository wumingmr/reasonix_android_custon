package sessioncatalog

import "context"

// Progress is a catalog mutation too. In particular, it must not acquire a
// second SQLite write lock while metadata synchronization owns a transaction.
func (c *Catalog) updateDirectoryScanProgress(ctx context.Context, path string, generation int64, total int) error {
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	if c.testScanProgressWriteHook != nil {
		c.testScanProgressWriteHook()
	}
	_, err := c.db.ExecContext(ctx, `UPDATE catalog_directories SET indexed=? WHERE path_key=? AND scan_generation=?`, total, c.pathKey(path), generation)
	if err == nil && c.opts.MetadataOnly {
		// Unchanged metadata batches only mark presence, so progress must not
		// depend on a redundant session/topic publication to become visible.
		c.refreshCounts(ctx)
	}
	return err
}

func (c *Catalog) failDirectoryScan(ctx context.Context, path string, scanErr error) {
	c.mutationMu.Lock()
	_, _ = c.db.ExecContext(ctx, `UPDATE catalog_directories SET state='degraded',error=? WHERE path_key=?`, scanErr.Error(), c.pathKey(path))
	c.mutationMu.Unlock()
	c.statusMu.Lock()
	c.status.State = StateDegraded
	c.status.LastError = scanErr.Error()
	c.statusMu.Unlock()
}
