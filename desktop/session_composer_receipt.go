package main

import (
	"encoding/json"
	"reasonix/internal/sessioninbox"
)

// A queued follow-up has an inbox receipt before it has any user-message row.
// Missing/expired receipts remain unknown, and this lookup never enqueues work.
func (a *App) composerGuidanceAccepted(view SessionComposerState) (bool, error) {
	var request struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(view.SubmissionRequest), &request); err != nil {
		return false, err
	}
	if request.Kind != "guidance" {
		return false, nil
	}
	meta := a.metaForDraftSession(view.Ref.SessionID)
	if meta == nil {
		return false, nil
	}
	_, ctrl := a.tabAndCtrlByID(meta.ID)
	reader, ok := ctrl.(interface {
		LookupInboxReceiptForSession(string, string) (sessioninbox.InboxReceipt, bool, error)
	})
	if !ok {
		return false, nil
	}
	receipt, found, err := reader.LookupInboxReceiptForSession(sessionRoute(view.Ref.SessionID), view.SubmissionID)
	return found && receipt.ItemID != "", err
}
