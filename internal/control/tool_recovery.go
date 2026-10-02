package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reasonix/internal/agent"

	"reasonix/internal/provider"
)

type ToolRecoverySnapshot struct {
	Silent       bool                         `json:"silent"`
	Statistics   agent.ToolRecoveryStatistics `json:"statistics"`
	SessionPath  string                       `json:"sessionPath"`
	RuntimeEpoch string                       `json:"runtimeEpoch"`
	Revision     string                       `json:"revision"`
	Calls        []provider.ToolCallRecord    `json:"calls"`
	RetryEnabled bool                         `json:"retryEnabled"`
	Retired      bool                         `json:"retired"`
}

type ToolRecoveryRequest struct {
	SessionPath  string `json:"sessionPath"`
	RuntimeEpoch string `json:"runtimeEpoch"`
	Revision     string `json:"revision"`
	AttemptID    string `json:"attemptId"`
	InspectionID string `json:"inspectionId"`
	Action       string `json:"action"` // inspect | confirm | reject | retry
}

func (c *Controller) ToolRecoverySnapshot() ToolRecoverySnapshot {
	view := ToolRecoverySnapshot{SessionPath: c.SessionPath(), RuntimeEpoch: c.RuntimeStateSnapshot().RuntimeEpoch, Calls: []provider.ToolCallRecord{}, Retired: true}
	if c.executor != nil {
		view.Calls = c.executor.PendingToolRecovery()
		view.Statistics = c.executor.ToolRecoveryStatistics()
		view.Silent = c.executor.SilentToolRecovery()
	}
	// Raw parameters stay in the session. Frontends get immutable identities
	// and inspection facts, never an executable payload supplied by the UI.
	for i := range view.Calls {
		view.Calls[i].Arguments = nil
	}
	bytes, _ := json.Marshal(view)
	sum := sha256.Sum256(bytes)
	view.Revision = hex.EncodeToString(sum[:])
	return view
}

// ResolveToolRecovery is a wire-compatible retired endpoint. Historical facts
// remain queryable, but no UI or client can confirm, reject, inspect, or replay
// an operation through the host.
func (c *Controller) ResolveToolRecovery(_ context.Context, _ ToolRecoveryRequest) (ToolRecoverySnapshot, error) {
	view := c.ToolRecoverySnapshot()
	return view, fmt.Errorf("tool_recovery_retired: historical execution facts are read-only; inspect external state and invoke tools normally if further work is needed")
}
