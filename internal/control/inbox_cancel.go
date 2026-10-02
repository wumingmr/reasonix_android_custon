package control

import "strings"

// InboxCancelResult is the authoritative receipt for a cancel+withdraw
// operation. Only IDs in DiscardedItemIDs are safe for a frontend to restore
// into its draft.
type InboxCancelResult struct {
	DiscardedItemIDs []string
	Warning          string
}

// CancelWithInboxItems stops the active turn and discards only the durable
// pending items explicitly owned by the cancelling frontend. Admission is
// paused around the batch deletion so TurnDone cannot race a cancelled item
// into a new provider turn. Unrelated inbox items remain intact.
func (c *Controller) CancelWithInboxItems(ids []string, source string) error {
	_, err := c.CancelWithInboxItemsResult(ids, source)
	return err
}

// CancelWithInboxItemsResult serializes withdrawal against every inbox
// admission path and returns exactly the durable messages that were removed.
// A consumed/running item is intentionally absent from the receipt.
func (c *Controller) CancelWithInboxItemsResult(ids []string, source string) (InboxCancelResult, error) {
	result := InboxCancelResult{DiscardedItemIDs: []string{}}
	// Stopping maintenance never withdraws queued messages. Those messages were
	// not part of the summary input and remain durable for post-maintenance
	// dispatch.
	if operationID, present, _ := c.signalMaintenanceCancel(); present {
		c.recordLifecycle("cancel_requested", source, operationID, 0, "")
		c.recordLifecycle("cancel_acknowledged", source, operationID, 0, "")
		return result, nil
	}
	c.inbox.admissionMu.Lock()
	defer c.inbox.admissionMu.Unlock()
	// Capture and signal the foreground owner before touching the inbox store.
	// A blocked sidecar or filesystem cannot delay Stop reaching the model/tool
	// context. Status persistence and Goal pausing run after the inbox mutation.
	turnID, cancelled := c.cancelTurnLocked()
	c.recordLifecycle("cancel_requested", source, turnID, 0, "")
	defer c.recordLifecycle("cancel_acknowledged", source, turnID, 0, "")
	defer c.finishCancel(turnID, cancelled)
	st, err := c.ensureInbox()
	if err != nil {
		return result, err
	}
	wasPaused := st.Snapshot().Paused
	if err := st.SetPaused(true); err != nil {
		return result, err
	}
	discarded, err := st.DiscardPendingItemsOwnedResult(ids, strings.TrimSpace(source))
	if err != nil {
		// Keep the inbox paused for inspection if an item already crossed the
		// admission boundary. Cancellation still stops that in-flight turn.
		return result, err
	}
	result.DiscardedItemIDs = discarded
	if !wasPaused {
		if err := st.SetPaused(false); err != nil {
			result.Warning = "The turn was stopped, but the message queue remains paused. Review it before resuming."
			return result, nil
		}
	}
	return result, nil
}
