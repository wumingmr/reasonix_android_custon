package main

import (
	"fmt"
	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"strings"
)

func historyLocalOnlyRows(m provider.Message) ([]HistoryMessage, bool) {
	if !m.LocalOnly {
		return nil, false
	}
	if m.ReadPause != nil {
		return []HistoryMessage{{Role: "notice", Code: event.TurnOutcomeIncompleteRead, Level: "info", ReadPause: m.ReadPause}}, true
	}
	if m.ReadCompletion != nil {
		return []HistoryMessage{{Role: "notice", Code: "read_completion", Level: "info", Content: "Partial read coverage was accepted for this turn.", Detail: formatReadCompletionDetail(m.ReadCompletion), ReadCompletion: m.ReadCompletion}}, true
	}
	if len(m.ProtocolRecovery) > 0 {
		if r, ok := provider.DecodeProtocolRecovery(m.ProtocolRecovery); ok && r.State == "pending" {
			return []HistoryMessage{{Role: "notice", Code: "protocol_recovery", Level: "info", Pending: true, ProtocolRecovery: &provider.ProtocolRecoveryAction{ID: r.ID}}}, true
		}
		return nil, true
	}
	if readiness := agent.HistoricalChecks(m.FinalReadinessRecovery); readiness != nil {
		return []HistoryMessage{{Role: "notice", Code: agent.HistoricalChecksNoticeCode, Level: "info",
			Content: agent.HistoricalChecksNoticeText, Readiness: readiness}}, true
	}
	rows, handled := historySteerRows(m.Content, true)
	for i := range rows {
		rows[i].MessageID = m.ID
	}
	return rows, handled
}

func formatReadCompletionDetail(r *provider.ReadCompletion) string {
	if r == nil {
		return ""
	}
	parts := make([]string, 0, len(r.Reads))
	for _, read := range r.Reads {
		parts = append(parts, fmt.Sprintf("%s · %s · covered=%v", read.Path, read.Verdict, read.Covered))
	}
	return strings.Join(parts, "\n")
}
