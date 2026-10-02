//go:build live

package agent

import (
	"encoding/json"
	"testing"
)

// External inspection establishes current state without rewriting historical
// execution facts through the retired recovery actions.
func snapshotLiveWriteHistory(t *testing.T, a *Agent) string {
	t.Helper()
	pending := a.PendingToolRecovery()
	if len(pending) != 1 || pending[0].ReadOnly {
		t.Fatalf("expected one unresolved write, got %d recovery records", len(pending))
	}
	data, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
