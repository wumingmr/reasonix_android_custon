package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"reasonix/internal/sessioninbox"
)

// InboxQueueRequest is the additive, session-fenced queue editing protocol.
type InboxQueueRequest struct {
	Kind           string  `json:"kind"`
	ItemID         string  `json:"itemId,omitempty"`
	Text           string  `json:"text,omitempty"`
	ContentVersion string  `json:"contentVersion,omitempty"`
	BeforeItemID   *string `json:"beforeItemId"`
	QueueRevision  int64   `json:"queueRevision"`
	Paused         bool    `json:"paused,omitempty"`
	TurnID         string  `json:"turnId,omitempty"`
	Display        string  `json:"display,omitempty"`
	IdempotencyKey string  `json:"idempotencyKey,omitempty"`
}

type InboxQueueEdit struct {
	ID             string   `json:"id"`
	Text           string   `json:"text"`
	ContentVersion string   `json:"contentVersion"`
	References     []string `json:"references"`
}

type InboxQueueResult struct {
	Outcome  string                     `json:"outcome"`
	Reason   string                     `json:"reason,omitempty"`
	Snapshot sessioninbox.InboxSnapshot `json:"snapshot"`
	Edit     *InboxQueueEdit            `json:"edit,omitempty"`
	Receipt  *sessioninbox.InboxReceipt `json:"receipt,omitempty"`
}

// InboxQueue pins one store for the whole operation. Never re-resolve the
// controller's current session after reference preparation or an async write.
func (c *Controller) InboxQueue(path string, req InboxQueueRequest) (InboxQueueResult, error) {
	st, err := c.ensureInbox()
	if err != nil {
		return InboxQueueResult{}, err
	}
	if path == "" || st.SessionPath() != path {
		return InboxQueueResult{Outcome: "unavailable", Reason: "session_changed"}, nil
	}
	result := InboxQueueResult{Outcome: "applied"}
	switch req.Kind {
	case "snapshot":
		result.Outcome = "unchanged"
	case "read":
		result.Edit, err = readInboxQueueEdit(st, req.ItemID)
	case "edit":
		err = c.editInboxQueue(st, req)
	case "move":
		err = st.MoveItemBefore(req.ItemID, req.BeforeItemID, req.QueueRevision)
	case "delete":
		err = st.DeleteItem(req.ItemID)
	case "pause":
		err = st.SetPaused(req.Paused)
	case "retry":
		err = st.RetryItem(req.ItemID)
	case "steer":
		if req.TurnID == "" {
			return result, fmt.Errorf("turnId is required")
		}
		_, err = c.trySteerInboxItemForSession(req.ItemID, req.TurnID, path)
	case "enqueue_steer":
		if req.TurnID == "" || req.IdempotencyKey == "" {
			return result, fmt.Errorf("turnId and idempotencyKey are required")
		}
		var receipt sessioninbox.InboxReceipt
		receipt, err = c.TryEnqueueAndSteerForTurn(req.TurnID, InboxRequest{ExpectedSessionPath: path, Display: req.Display, Raw: req.Text, Submit: req.Text, Idempotency: req.IdempotencyKey, Source: "desktop"})
		if err == nil {
			result.Receipt = &receipt
		}
	default:
		return result, fmt.Errorf("unknown inbox queue operation %q", req.Kind)
	}
	result.Snapshot = st.Snapshot()
	if err != nil {
		result.Outcome, result.Reason = inboxQueueFailure(err)
		if result.Reason == "" {
			return result, err
		}
		return result, nil
	}
	if req.Kind != "read" && req.Kind != "snapshot" && !result.Snapshot.Paused {
		c.maybeDispatchInbox()
	}
	return result, nil
}

func inboxQueueFailure(err error) (string, string) {
	for _, item := range []struct {
		err    error
		reason string
	}{
		{sessioninbox.ErrContentChanged, "content_changed"},
		{sessioninbox.ErrOrderChanged, "order_changed"},
		{sessioninbox.ErrAnchorMissing, "anchor_missing"},
	} {
		if errors.Is(err, item.err) {
			return "conflict", item.reason
		}
	}
	for _, item := range []struct {
		err    error
		reason string
	}{
		{sessioninbox.ErrNotFound, "item_missing"},
		{sessioninbox.ErrInvalidState, "item_not_pending"},
		{sessioninbox.ErrSchemaReadonly, "read_only"},
		{errInboxStructuredEdit, "structured_input"},
		{sessioninbox.ErrClosed, "session_changed"},
		{ErrInboxSessionChanged, "session_changed"},
	} {
		if errors.Is(err, item.err) {
			return "unavailable", item.reason
		}
	}
	return "", ""
}

var errInboxStructuredEdit = errors.New("structured invocation cannot be edited as plain text")

func readInboxQueueEdit(st *sessioninbox.Store, id string) (*InboxQueueEdit, error) {
	meta, env, err := st.ReadItem(id)
	if err != nil {
		return nil, err
	}
	if meta.State != sessioninbox.StateQueued && meta.State != sessioninbox.StateBlocked && meta.State != sessioninbox.StateUncertain {
		return nil, sessioninbox.ErrInvalidState
	}
	if env.Invocation != nil || len(env.Invocations) > 0 {
		return nil, errInboxStructuredEdit
	}
	refs := append([]string{}, env.Attachments...)
	refs = append(refs, env.ExplicitRefs...)
	for _, ref := range env.Refs {
		refs = append(refs, firstNonEmptyStr(ref.DisplayPath, ref.Path))
	}
	for _, input := range env.ImageInputs {
		if input.Attachment != nil {
			refs = append(refs, input.Attachment.DisplayName)
		}
	}
	return &InboxQueueEdit{ID: id, Text: env.SubmitText, ContentVersion: sessioninbox.ContentVersion(meta), References: refs}, nil
}

func (c *Controller) editInboxQueue(st *sessioninbox.Store, req InboxQueueRequest) error {
	if strings.TrimSpace(req.Text) == "" {
		return sessioninbox.ErrEmpty
	}
	meta, env, err := st.ReadItem(req.ItemID)
	if err != nil {
		return err
	}
	if req.ContentVersion == "" || sessioninbox.ContentVersion(meta) != req.ContentVersion {
		return sessioninbox.ErrContentChanged
	}
	if env.Invocation != nil || len(env.Invocations) > 0 {
		return errInboxStructuredEdit
	}
	// Keep the original immutable reference bytes when only prose changes.
	// A changed @ reference is resolved by the existing authorization path.
	if !slices.Equal(parseRefTokens(env.SubmitText), parseRefTokens(req.Text)) {
		if err := c.freezeInboxEnvelopeReferences(context.Background(), &env, req.Text, env.ExplicitRefs); err != nil {
			return err
		}
	}
	env.DisplayText, env.RawText, env.SubmitText = req.Text, req.Text, req.Text
	_, err = st.UpdateItemIfVersion(req.ItemID, env, req.ContentVersion)
	return err
}
