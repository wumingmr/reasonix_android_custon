package control

import (
	"encoding/json"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/secrets"
	"reasonix/internal/session"
)

// One bounded, optional record shares the terminal commit. Cold exports can
// inspect it without a resident controller; older readers skip it safely.
func (c *Controller) providerDiagnosticEvent(e event.Event) (*session.Event, error) {
	status := e.Status
	if status == "" {
		status = terminalTurnStatus(e)
	}
	if status != event.TurnFailed && status != event.TurnInterrupted && status != event.TurnRecoveryRequired {
		return nil, nil
	}
	failure := e.Diagnostic
	if failure == nil {
		failure = provider.DiagnoseFailure(e.Err)
	}
	requests, dropped, truncated := c.providerDiagnosticTurnSnapshot(e.TurnID)
	if len(requests) == 0 && failure == nil && (dropped == nil || *dropped == 0) {
		return nil, nil
	}
	payload, err := json.Marshal(struct {
		SchemaVersion int                         `json:"schemaVersion"`
		Failure       *provider.FailureDiagnostic `json:"failure,omitempty"`
		RequestLimit  int                         `json:"requestLimit"`
		Requests      []providerDiagnostic        `json:"requests"`
		Dropped       *uint64                     `json:"dropped,omitempty"`
		Truncated     bool                        `json:"truncated"`
	}{1, failure, 128, requests, dropped, truncated})
	if err != nil {
		return nil, err
	}
	payload, err = secrets.RedactJSON(payload)
	if err != nil {
		return nil, err
	}
	return &session.Event{Kind: "diagnostic/provider", Optional: true, Payload: payload}, nil
}
