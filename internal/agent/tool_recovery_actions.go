package agent

import (
	"context"
	"fmt"

	"reasonix/internal/provider"
)

var errToolRecoveryRetired = fmt.Errorf("tool_recovery_retired: historical execution facts are read-only; invoke tools normally after checking external state")

func (a *Agent) recoveryCall(attempt string) (provider.ToolCall, error) {
	for _, message := range a.Session().Snapshot() {
		for _, call := range message.ToolCalls {
			if call.Recovery != nil && call.Recovery.Identity.AttemptID == attempt {
				return call, nil
			}
		}
	}
	return provider.ToolCall{}, fmt.Errorf("recovery action is stale or unavailable")
}

// InspectToolRecovery remains for binary compatibility. Inspection used to
// mutate the record and authorize a follow-up action; callers now receive the
// immutable historical fact and a stable retirement error.
func (a *Agent) InspectToolRecovery(_ context.Context, attempt string) (provider.ToolCallRecord, error) {
	call, err := a.recoveryCall(attempt)
	if err != nil {
		return provider.ToolCallRecord{}, err
	}
	return *call.Recovery, errToolRecoveryRetired
}

func (*Agent) ResolveToolRecovery(_, _, _ string) error { return errToolRecoveryRetired }

func (*Agent) RetryToolRecovery(_ context.Context, _, _ string) error { return errToolRecoveryRetired }
