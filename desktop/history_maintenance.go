package main

import (
	"encoding/json"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"slices"
)

func maintenanceHistoryRow(op *event.SessionOperationInfo) HistoryMessage {
	return HistoryMessage{Role: "compaction", Pending: op.Status == "running" || op.Status == "cancelling" || op.Status == "finalizing", Trigger: "manual",
		OperationID: op.OperationID, OperationRevision: op.OperationRevision, RuntimeEpoch: op.RuntimeEpoch,
		OperationKind: op.Kind, OperationStatus: op.Status, OperationActivity: op.Activity,
		ErrorCode: op.ErrorCode, Detail: op.Detail, Applied: op.Applied,
		InputTokens: op.InputTokens, ResultTokens: op.ResultTokens,
		Messages: op.Messages, Summary: op.Summary, Archive: op.Archive}
}

func maintenanceHistoryMessage(m provider.Message) []HistoryMessage {
	var op event.SessionOperationInfo
	if json.Unmarshal([]byte(m.Content), &op) != nil || op.OperationID == "" {
		return nil
	}
	row := maintenanceHistoryRow(&op)
	row.RecordID = m.ID
	return []HistoryMessage{row}
}

func upsertMaintenancePreview(rows []HistoryMessage, op *event.SessionOperationInfo) []HistoryMessage {
	if op == nil || op.OperationID == "" {
		return rows
	}
	row := maintenanceHistoryRow(op)
	for i, prior := range slices.Backward(rows) {
		if prior.OperationID == op.OperationID {
			rows[i] = row
			return rows
		}
	}
	return append(rows, row)
}
