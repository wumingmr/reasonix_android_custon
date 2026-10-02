package transcript

import (
	"slices"
	"testing"

	"reasonix/internal/provider"
)

func TestRestoreCheckpointRepairsLegacyMissingRecordIdentities(t *testing.T) {
	state := Checkpoint{
		Version: ProtocolVersion, Identity: testIdentity, CoveredThroughSeq: 7,
		Records: []Message{
			{Role: "notice", Content: "first"},
			{Role: "notice", Content: "second"},
			{Role: "assistant", MessageID: "answer", Content: "answer"},
			{Role: "tool", ToolCallID: "call", Content: "result"},
		},
	}
	want := []string{"view:checkpoint:7:0", "view:checkpoint:7:1", "m:answer", "tool:call"}
	for range 2 {
		projection, err := RestoreCheckpoint(state, testIdentity)
		if err != nil {
			t.Fatal(err)
		}
		messages := projection.buffer.Messages()
		got := make([]string, len(messages))
		for index := range messages {
			got[index] = messages[index].RecordID
		}
		if !slices.Equal(got, want) {
			t.Fatalf("repaired identities = %v, want %v", got, want)
		}
	}
}

func TestRestoreCheckpointStillRejectsNonEmptyDuplicateIdentity(t *testing.T) {
	state := Checkpoint{Version: ProtocolVersion, Identity: testIdentity, Records: []Message{
		{RecordID: "duplicate", Role: "notice", Content: "first"},
		{RecordID: "duplicate", Role: "notice", Content: "second"},
	}}
	if _, err := RestoreCheckpoint(state, testIdentity); err == nil {
		t.Fatal("duplicate checkpoint identity was accepted")
	}
}

func TestRepairCheckpointToolResultsRestoresUniqueFormalIdentityAndMetadata(t *testing.T) {
	execution := &provider.ToolExecution{Kind: "shell", State: "completed", DurationMs: 42}
	records := []Message{{RecordID: "tool:call-1", Role: "tool", ToolCallID: "call-1", ToolName: "PowerShell",
		Content: "event display", ToolResultError: "", HistoryTurn: 1}}
	canonical := []Message{
		{RecordID: "m:user-1", MessageID: "user-1", Role: "user"},
		{RecordID: "tool:call-1", MessageID: "result-1", Role: "tool", ToolCallID: "call-1", ToolName: "PowerShell",
			Content: "canonical body", CreatedAt: 123, Execution: execution, ToolResultArchived: true},
	}

	got, stats := RepairCheckpointToolResults(records, canonical)
	if stats.Repaired != 1 || stats.Missing != 0 || stats.Conflicts != 0 {
		t.Fatalf("repair stats = %+v", stats)
	}
	if len(got) != 1 || got[0].MessageID != "result-1" || got[0].RecordID != "tool:call-1" || got[0].Content != "event display" ||
		got[0].HistoryTurn != 1 || got[0].CreatedAt != 123 || got[0].Execution != execution || !got[0].ToolResultArchived {
		t.Fatalf("repaired checkpoint row = %+v", got)
	}
	if records[0].MessageID != "" {
		t.Fatal("repair mutated the caller-owned checkpoint slice")
	}
}

func TestRepairCheckpointToolResultsRefusesAmbiguousOrConflictingIdentity(t *testing.T) {
	records := []Message{
		{RecordID: "tool:ambiguous", Role: "tool", ToolCallID: "ambiguous", HistoryTurn: 1},
		{RecordID: "tool:wrong-turn", Role: "tool", ToolCallID: "wrong-turn", HistoryTurn: 2},
	}
	canonical := []Message{
		{MessageID: "user-1", Role: "user"},
		{MessageID: "a", Role: "tool", ToolCallID: "ambiguous"},
		{MessageID: "b", Role: "tool", ToolCallID: "ambiguous"},
		{MessageID: "c", Role: "tool", ToolCallID: "wrong-turn"},
	}

	got, stats := RepairCheckpointToolResults(records, canonical)
	if stats.Repaired != 0 || stats.Conflicts != 1 || stats.Missing != 1 {
		t.Fatalf("repair stats = %+v", stats)
	}
	if got[0].MessageID != "" || got[1].MessageID != "" {
		t.Fatalf("inconclusive rows were guessed: %+v", got)
	}
}

func TestRepairCheckpointToolResultsDoesNotReuseOccupiedFormalIdentity(t *testing.T) {
	records := []Message{
		{RecordID: "m:result", MessageID: "result", Role: "tool", ToolCallID: "call"},
		{RecordID: "tool:call", Role: "tool", ToolCallID: "call"},
	}
	canonical := []Message{{RecordID: "m:result", MessageID: "result", Role: "tool", ToolCallID: "call"}}
	got, stats := RepairCheckpointToolResults(records, canonical)
	if stats.Repaired != 0 || stats.Conflicts != 1 || got[1].MessageID != "" {
		t.Fatalf("occupied formal identity was reused: stats=%+v rows=%+v", stats, got)
	}
	if !NeedsToolResultRepair(records) || NeedsToolResultRepair(records[:1]) {
		t.Fatal("repair fast-path predicate does not match missing tool identities")
	}
}
