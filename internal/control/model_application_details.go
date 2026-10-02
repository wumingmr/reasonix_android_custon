package control

import "reasonix/internal/jobs"

// ModelApplicationDetails contains only user-facing state; never credentials,
// request bodies, internal paths, or mutable resolver configuration.
type ModelApplicationDetails struct {
	Code                    string      `json:"code"`
	RuntimeIdentity         string      `json:"runtimeIdentity"`
	ConfirmationToken       string      `json:"confirmationToken,omitempty"`
	AppliedRevision         string      `json:"appliedRevision"`
	DesiredRevision         string      `json:"desiredRevision"`
	Model                   string      `json:"model"`
	ConnectionTarget        string      `json:"connectionTarget,omitempty"`
	BlockingJobs            []jobs.View `json:"blockingJobs"`
	CanUseApplied           bool        `json:"canUseApplied"`
	ContinuationUnavailable string      `json:"continuationUnavailable,omitempty"`
	Applying                bool        `json:"applying"`
	AvailableActions        []string    `json:"availableActions"`
}

func (d *ModelApplicationDetails) SetAvailableActions() {
	d.AvailableActions = []string{}
	if d.Applying {
		return
	}
	d.AvailableActions = append(d.AvailableActions, "retry")
	if len(d.BlockingJobs) > 0 {
		d.AvailableActions = append(d.AvailableActions, "view_blockers", "stop_selected")
	}
	if d.CanUseApplied {
		d.AvailableActions = append(d.AvailableActions, "applied_once")
	}
}
