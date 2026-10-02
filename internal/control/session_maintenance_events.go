package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// persistMaintenanceOperation has its own durability barrier: maintenance is
// deliberately outside a turn, so the ordinary TurnDone flush cannot own it.
func (c *Controller) persistMaintenanceOperation(e event.Event) error {
	events, err := sessionMaintenanceEvents(e)
	if err != nil {
		return err
	}
	if !c.sessionEventCommitAllowed() {
		return session.ErrStaleExecution
	}
	store := c.sessionEventStore()
	if store == nil {
		if c.sessionEngineEnabled() {
			return session.ErrSessionNotRunning
		}
		return nil
	}
	op := e.SessionOperation
	c.turnEvents.commitMu.Lock()
	commit, err := c.appendSessionBatch(context.Background(), store, session.Batch{
		OperationID: fmt.Sprintf("session-maintenance:%s:%d", op.OperationID, op.OperationRevision), Events: events,
	})
	c.turnEvents.commitMu.Unlock()
	if err != nil {
		return err
	}
	receipt, err := store.Flush(context.Background())
	if err != nil {
		return err
	}
	if receipt.DurableSequence < commit.LastSequence() {
		return fmt.Errorf("maintenance operation is not durable through sequence %d", commit.LastSequence())
	}
	return nil
}

// sessionMaintenanceEvents persists one replaceable display record. The
// diagnostic is optional, excluded from Projection.ModelMessages, and safe for
// older readers to ignore.
func sessionMaintenanceEvents(e event.Event) ([]session.Event, error) {
	if e.SessionOperation == nil || e.SessionOperation.OperationID == "" {
		return nil, errors.New("session operation has no identity")
	}
	content, err := json.Marshal(e.SessionOperation)
	if err != nil {
		return nil, err
	}
	record, err := json.Marshal(provider.Message{
		ID:   "maintenance:" + e.SessionOperation.OperationID,
		Role: provider.Role("compaction"), Content: string(content),
	})
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{
		"type": "session-maintenance-v1", "displayRecord": json.RawMessage(record),
	})
	if err != nil {
		return nil, err
	}
	return []session.Event{{Kind: "diagnostic", Optional: true, Payload: payload}}, nil
}
