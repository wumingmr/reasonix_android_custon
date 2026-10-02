package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

func TestMaintenanceHistoryKeepsProgressPendingUntilRuntimeSync(t *testing.T) {
	op := event.SessionOperationInfo{
		OperationID: "maintenance-history", OperationRevision: 3, RuntimeEpoch: "runtime-old",
		Kind: "compact", Activity: "running", Status: "running", InputTokens: 1200,
	}
	payload, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	rows := historyMessages([]provider.Message{{
		ID: "maintenance:maintenance-history", Role: provider.Role("compaction"), Content: string(payload),
	}}, strings.TrimSpace)
	assertPendingMaintenanceHistory(t, rows)
}

func TestPreviewMaintenanceHistoryKeepsProgressPendingUntilRuntimeSync(t *testing.T) {
	op := event.SessionOperationInfo{
		OperationID: "maintenance-history", OperationRevision: 3, RuntimeEpoch: "runtime-old",
		Kind: "compact", Activity: "running", Status: "running", InputTokens: 1200,
	}
	record, err := json.Marshal(previewEventRecord{Kind: "session_operation", SessionOperation: &op})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, append(record, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, ok, err := previewEventSessionMessages(path)
	if err != nil || !ok {
		t.Fatalf("previewEventSessionMessages = ok %v, err %v", ok, err)
	}
	assertPendingMaintenanceHistory(t, rows)
}

func assertPendingMaintenanceHistory(t *testing.T, rows []HistoryMessage) {
	t.Helper()
	if len(rows) != 1 {
		t.Fatalf("history rows = %+v", rows)
	}
	row := rows[0]
	if !row.Pending || row.OperationStatus != "running" || row.OperationID != "maintenance-history" ||
		row.OperationRevision != 3 || row.RuntimeEpoch != "runtime-old" || row.InputTokens != 1200 {
		t.Fatalf("maintenance history row = %+v", row)
	}
}
