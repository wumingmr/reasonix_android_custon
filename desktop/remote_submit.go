package main

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reasonix/internal/control"
	"reasonix/internal/serve"
	"reasonix/internal/sessioninbox"
	"strings"
	"time"
)

func (a *App) SubmitRemoteTab(tabID, text string) error {
	return a.SubmitRemoteTabWithSubmission(tabID, text, "")
}

// remoteBusySubmitError reports whether the serve refused a foreground submit
// because a turn is still active; its answer directs clients to the durable
// inbox follow-up endpoint.
func remoteBusySubmitError(err error) bool {
	var statusErr *serveHTTPStatusError
	return errors.As(err, &statusErr) && statusErr.statusCode == http.StatusConflict &&
		strings.Contains(statusErr.message, serve.SubmitBusyMessage)
}

// remoteSessionChangedSubmitError reports the serve's expected-session fence:
// the submit raced a session switch or a session re-adoption. Nothing started,
// and the same submit succeeds once the route settles.
func remoteSessionChangedSubmitError(err error) bool {
	var statusErr *serveHTTPStatusError
	return errors.As(err, &statusErr) && statusErr.statusCode == http.StatusConflict &&
		strings.Contains(statusErr.message, serve.SubmitSessionChangedMessage)
}

// remoteSubmitRetryDelaysMs paces a submit that raced a session change. The
// window is a route settle (resume commit plus buffered-frame drain), so a few
// short retries cover it; anything longer is reported as a transient outcome
// the composer holds and retries with the rest of the durable queue.
var remoteSubmitRetryDelaysMs = []int{120, 250, 500}

// submitWithRouteRetry posts one foreground submit, re-resolving the route and
// retrying while the serve answers that the active session changed. A response
// that is not that fence (2xx, busy, or any other refusal) returns immediately.
func (a *App) submitWithRouteRetry(tabID string, attempt func(client *http.Client, base, expectedPath string) error) error {
	var lastErr error
	for round := 0; ; round++ {
		client, base, expectedPath, err := a.remoteTabCommandTarget(tabID)
		if err != nil {
			return err
		}
		lastErr = attempt(client, base, expectedPath)
		if lastErr == nil || !remoteSessionChangedSubmitError(lastErr) || round >= len(remoteSubmitRetryDelaysMs) {
			break
		}
		time.Sleep(time.Duration(remoteSubmitRetryDelaysMs[round]) * time.Millisecond)
	}
	if lastErr != nil && remoteSessionChangedSubmitError(lastErr) {
		// The route did not settle in the retry window: report a transient
		// outcome so the composer holds the message visibly instead of showing
		// a failure the user cannot act on.
		return inboxTargetTransient(lastErr)
	}
	return lastErr
}

// queuedFollowupError reports a submit that could not start a turn because the
// previous one is still running: the message is durably queued as the follow-up
// the serve asks for. The receipt rides RPCErrorData so the renderer can show
// the visible queue entry immediately instead of a failure.
type queuedFollowupError struct {
	receipt InboxReceiptView
}

func (e *queuedFollowupError) Error() string { return "reasonix_error:queued_followup" }

func (e *queuedFollowupError) RPCErrorData() map[string]any {
	return map[string]any{"queuedFollowup": e.receipt}
}

// queueBusyFollowup converts a busy-window submit into the durable inbox
// follow-up the serve asks for, so a message typed while the previous turn is
// finishing is queued for the next turn instead of surfacing a conflict. The
// submission id doubles as the inbox idempotency key, so a retried submit can
// never double-queue the same message.
func (a *App) queueBusyFollowup(tabID, text, submissionID string) (*queuedFollowupError, error) {
	idempotency := strings.TrimSpace(submissionID)
	if idempotency == "" {
		raw := make([]byte, 12)
		if _, err := cryptorand.Read(raw); err != nil {
			return nil, err
		}
		idempotency = "submit-busy-" + hex.EncodeToString(raw)
	}
	receipt, err := a.enqueueInbox(tabID, sessioninbox.IntentFollowup, text, text, nil, idempotency, false)
	if err != nil {
		return nil, err
	}
	// Remote inbox enqueues bypass the local change notification; tell the
	// renderer so the queue strip reconciles this item.
	a.emitInboxChanged(tabID)
	return &queuedFollowupError{receipt: receipt}, nil
}

func (a *App) SubmitRemoteTabWithSubmission(tabID, text, submissionID string) error {
	// Report the connection state before capability negotiation so a tab that
	// has not finished bootstrap is never misdiagnosed as a legacy Serve.
	if _, _, _, err := a.remoteTabCommandTarget(tabID); err != nil {
		return err
	}
	if err := a.requireRemoteExecutionProtocol(tabID); err != nil {
		return err
	}
	if err := a.requireRemotePermissionPresets(tabID); err != nil {
		return err
	}
	if a.remoteModelApplicationReady(tabID) {
		return a.SubmitRemoteTabWithModelApplication(tabID, text, submissionID, control.ModelApplicationChoice{Mode: "latest"})
	}
	for {
		revision, admittedGen, err := a.ensureRemoteModelSettings(tabID)
		if err != nil {
			return &submissionNotAcceptedError{cause: err}
		}
		if !a.remoteTabAdmissionCurrent(tabID, admittedGen) {
			continue
		}
		input := map[string]string{"input": text}
		if submissionID != "" {
			input["submissionId"] = submissionID
		}
		body, _ := json.Marshal(input)
		err = a.submitWithRouteRetry(tabID, func(client *http.Client, base, expectedPath string) error {
			ctx, cancel := commandContext(a)
			defer cancel()
			return servePostForSession(ctx, client, serveURL(base, "/submit"), body, expectedPath, revision)
		})
		if err != nil && remoteBusySubmitError(err) {
			// The turn is finishing; queue the message as a visible durable
			// follow-up and report it as queued rather than failed.
			if queued, queueErr := a.queueBusyFollowup(tabID, text, submissionID); queueErr == nil {
				return queued
			}
		}
		return err
	}
}
