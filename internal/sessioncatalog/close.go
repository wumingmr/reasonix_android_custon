package sessioncatalog

import (
	"context"

	"reasonix/internal/projectiondb"
)

func (c *Catalog) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.stopOnce.Do(func() {
		if c.workerCancel != nil {
			c.workerCancel()
		}
		close(c.stop)
		go func() {
			c.closeReadLeases()
			c.workers.Wait()
			projectiondb.CheckpointBeforeClose(context.Background(), c.db)
			c.closeErr = c.db.Close()
			c.statusMu.Lock()
			c.status.State = StateClosed
			c.statusMu.Unlock()
			close(c.closeDone)
		}()
	})
	select {
	case <-c.closeDone:
		return c.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
