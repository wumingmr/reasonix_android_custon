package main

import (
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

func TestHistoryMessagesUnappliedSteerKeepsMessageIdentity(t *testing.T) {
	const content = "[Mid-turn steer queued by the user. Do not treat this as a new task; use it only as additional guidance for the current task after completing the current step.]\nUse plan B"
	rows := historyMessages([]provider.Message{{ID: "queued", Role: provider.RoleTool, Content: content,
		ToolCallID: provider.LocalOnlyToolID, Name: provider.LocalOnlyToolName, LocalOnly: true}}, func(text string) string { return text })
	if len(rows) != 1 || rows[0].Role != "notice" || rows[0].MessageID != "queued" || rows[0].Code != event.NoticeCodeUnappliedSteer {
		t.Fatalf("unapplied steer history identity = %+v", rows)
	}
}
