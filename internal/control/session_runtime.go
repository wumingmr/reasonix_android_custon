package control

import (
	"context"

	"reasonix/internal/session"
)

func (c *Controller) appendSessionBatch(ctx context.Context, store *session.Session, batch session.Batch) (session.Commit, error) {
	if store == nil {
		return session.Commit{}, session.ErrSessionNotRunning
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return session.Commit{}, err
	}
	prepared, err := store.PrepareBatchContext(ctx, batch.OperationID, batch)
	if err != nil {
		return session.Commit{}, err
	}
	_, runtime, exclusive := c.v3Binding()
	if exclusive && runtime != nil && runtime.Session() == store {
		return runtime.CommitPreparedForExecution(c.ExecutionGeneration(), prepared)
	}
	return store.CommitPrepared(prepared)
}
