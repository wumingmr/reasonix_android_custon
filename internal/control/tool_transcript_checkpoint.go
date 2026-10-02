package control

import (
	"context"
	"log/slog"
	"reasonix/internal/agent"
)

// CheckpointSession implements agent.SessionCheckpointer. The boundary is
// intentionally semantic: ordinary todo, approval, assistant and turn-end
// events remain eligible for the write-behind batch.
func (c *Controller) CheckpointSession(ctx context.Context, boundary agent.SessionCheckpointBoundary) error {
	switch boundary {
	case agent.CheckpointBeforeModel, agent.CheckpointBeforeTopTool:
		if _, err := c.flushSessionEvents(ctx); err != nil {
			return err
		}
		return nil
	case agent.CheckpointUserAdmitted:
		// An event store recorded the message when it was admitted; only a
		// legacy transcript still holds it in memory alone.
		if c.sessionEventStore() != nil || c.SessionPath() == "" {
			return nil
		}
		if err := c.snapshot(false, false, false); err != nil {
			slog.Warn("controller: save admitted user message", "err", err)
		}
		return nil
	default:
		return nil
	}
}
