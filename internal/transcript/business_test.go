package transcript

import (
	"fmt"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/provider"
	"reasonix/internal/turnevent"
)

func businessFrame(t *testing.T, p *Projection, covered uint64, e event.Event) {
	t.Helper()
	wire := eventwire.ToWire(e)
	status := event.TurnInProgress
	if e.Kind == event.TurnDone {
		status = event.TurnCompleted
	}
	if err := p.ApplyFrame(turnevent.Envelope{SessionID: testIdentity.SessionID, RuntimeEpoch: testIdentity.RuntimeEpoch, TurnID: "turn", Kind: wire.Kind, Status: status, Event: wire}, covered); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptBusinessPublishesTheTurnIDItAssigns(t *testing.T) {
	p, initial := newFollowProjection(t)
	p.AcceptBusiness([]Message{
		{RecordID: "m:user", MessageID: "user", Role: "user", Content: "question"},
		{RecordID: "m:answer", MessageID: "answer", Role: "assistant", Content: "answer"},
		{RecordID: "m:kept", MessageID: "kept", Role: "user", Content: "other", TurnID: "kept-turn"},
	}, 1, "turn-7", false)
	suffix := followChanges(t, p, FollowRequest{Subscription: initial.Subscription, AfterRevision: initial.Snapshot.ProjectionRevision})
	if len(suffix.Changes) != 1 || len(suffix.Changes[0].Records) != 3 {
		t.Fatalf("published records = %+v", suffix.Changes)
	}
	records := suffix.Changes[0].Records
	if records[0].TurnID != "turn-7" || records[1].TurnID != "turn-7" || records[2].TurnID != "kept-turn" {
		t.Fatalf("published turn identity = %q %q %q", records[0].TurnID, records[1].TurnID, records[2].TurnID)
	}
	cut := snapshot(t, p)
	if len(cut.Records) != 3 || cut.Records[0].Message.TurnID != "turn-7" || cut.Records[1].Message.TurnID != "turn-7" || cut.Records[2].Message.TurnID != "kept-turn" {
		t.Fatalf("buffer turn identity diverged from the published change: %+v", cut.Records)
	}
}

func TestBusinessSettlementUpdatesStreamingRowWithoutDuplicateOrPendingState(t *testing.T) {
	p, err := NewProjection(testIdentity, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	businessFrame(t, p, 0, event.Event{Kind: event.StreamAttempt, MessageID: "answer", AttemptID: "answer", StreamAttempt: event.StreamAttemptInfo{ID: "answer", Action: event.StreamAttemptBegin}})
	businessFrame(t, p, 0, event.Event{Kind: event.Text, MessageID: "answer", AttemptID: "answer", Text: "partial"})
	p.AcceptBusiness([]Message{{RecordID: "m:answer", MessageID: "answer", Role: "assistant", Content: "complete answer", Reasoning: "complete thinking"}}, 1, "turn", false)
	businessFrame(t, p, 1, event.Event{Kind: event.Message, MessageID: "answer", AttemptID: "answer", Text: "complete answer", Reasoning: "complete thinking"})
	businessFrame(t, p, 1, event.Event{Kind: event.StreamAttempt, MessageID: "answer", AttemptID: "answer", StreamAttempt: event.StreamAttemptInfo{ID: "answer", Action: event.StreamAttemptCommit}})
	businessFrame(t, p, 1, event.Event{Kind: event.TurnDone})
	cut := snapshot(t, p)
	if len(cut.Records) != 1 {
		t.Fatalf("settlement duplicated streaming node: records=%d", len(cut.Records))
	}
	message := cut.Records[0].Message
	if message.Content != "complete answer" || message.Reasoning != "complete thinking" || message.Pending {
		t.Fatalf("settled content or pending state is wrong: %+v", message)
	}
	if len(cut.ActiveAttempts) != 0 || len(cut.ActiveRecords) != 0 {
		t.Fatalf("settlement retains active state: attempts=%d records=%d", len(cut.ActiveAttempts), len(cut.ActiveRecords))
	}
}

func TestToolResultCompletionPreservesCanonicalIdentityAndMetadata(t *testing.T) {
	p, err := NewProjection(testIdentity, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	execution := &provider.ToolExecution{Kind: "shell", State: "completed", DurationMs: 23}
	readCompletion := &provider.ReadCompletion{ID: "read-1", Omitted: 2}
	p.AcceptBusiness([]Message{{
		RecordID: "tool:call-1", MessageID: "result-1", Role: "tool", ToolCallID: "call-1", ToolName: "PowerShell",
		Content: "persisted", Source: "history", TurnID: "turn-1", HistoryTurn: 4, CreatedAt: 123,
		Execution: execution, ToolResultArchived: true, ReadCompletion: readCompletion,
	}}, 1, "turn-1", false)
	businessFrame(t, p, 1, event.Event{Kind: event.ToolResult, MessageID: "assistant-owner", Tool: event.Tool{
		ID: "call-1", Name: "PowerShell", Output: "completed", PresentedFiles: []provider.PresentedFile{{Path: "report.txt"}},
	}})

	cut := snapshot(t, p)
	if len(cut.Records) != 1 {
		t.Fatalf("tool completion duplicated canonical row: records=%d", len(cut.Records))
	}
	got := cut.Records[0].Message
	if got.RecordID != "tool:call-1" || got.MessageID != "result-1" || got.Source != "history" || got.TurnID != "turn-1" || got.HistoryTurn != 4 || got.CreatedAt != 123 {
		t.Fatalf("tool completion lost canonical identity/location: %+v", got)
	}
	if got.Execution == nil || got.Execution.Kind != execution.Kind || got.Execution.State != execution.State || got.Execution.DurationMs != execution.DurationMs ||
		!got.ToolResultArchived || got.ReadCompletion == nil || got.ReadCompletion.ID != readCompletion.ID || got.ReadCompletion.Omitted != readCompletion.Omitted {
		t.Fatalf("tool completion lost canonical metadata: %+v", got)
	}
	if got.Content != "completed" || len(got.PresentedFiles) != 1 || got.PresentedFiles[0].Path != "report.txt" {
		t.Fatalf("tool completion did not update event-owned fields: %+v", got)
	}
}

func TestToolResultEventThenCanonicalRecordKeepsOneStableRow(t *testing.T) {
	p, err := NewProjection(testIdentity, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	businessFrame(t, p, 0, event.Event{Kind: event.ToolResult, Tool: event.Tool{ID: "call-1", Name: "edit_file", Output: "event result"}})
	p.AcceptBusiness([]Message{{RecordID: "tool:call-1", MessageID: "result-1", Role: "tool", ToolCallID: "call-1",
		ToolName: "edit_file", Content: "canonical result", HistoryTurn: 2, Execution: &provider.ToolExecution{State: "completed"}}}, 1, "turn-1", false)
	businessFrame(t, p, 1, event.Event{Kind: event.ToolResult, Tool: event.Tool{ID: "call-1", Name: "edit_file", Output: "event result"}})

	cut := snapshot(t, p)
	if len(cut.Records) != 1 {
		t.Fatalf("event/formal handoff duplicated row: records=%d", len(cut.Records))
	}
	got := cut.Records[0].Message
	if got.RecordID != "tool:call-1" || got.MessageID != "result-1" || got.HistoryTurn != 2 || got.Execution == nil {
		t.Fatalf("event/formal handoff degraded canonical row: %+v", got)
	}
}

func TestUnappliedSteerNoticeAndCanonicalRecordShareOneRow(t *testing.T) {
	for _, eventFirst := range []bool{false, true} {
		p, err := NewProjection(testIdentity, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		const recordID = "m:queued:notice:0"
		warning := event.Event{Kind: event.Notice, Code: event.NoticeCodeUnappliedSteer,
			MessageID: "queued", Level: event.LevelWarn, Text: "Guidance was not applied:\nUse plan B"}
		formal := []Message{{RecordID: recordID, MessageID: "queued", Role: "notice", Code: event.NoticeCodeUnappliedSteer,
			Level: "warn", Content: warning.Text}}
		if eventFirst {
			businessFrame(t, p, 0, warning)
			p.AcceptBusiness(formal, 1, "turn", false)
		} else {
			p.AcceptBusiness(formal, 1, "turn", false)
			businessFrame(t, p, 1, warning)
		}
		businessFrame(t, p, 1, warning)
		cut := snapshot(t, p)
		if len(cut.Records) != 1 || cut.Records[0].ID != recordID || cut.Records[0].Message.MessageID != "queued" {
			t.Fatalf("eventFirst=%v duplicated unapplied steer: %+v", eventFirst, cut.Records)
		}
	}
}

func TestBusinessRowsWithoutCanonicalIdentityReceiveDistinctViewIdentity(t *testing.T) {
	p, err := NewProjection(testIdentity, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := p.Follow(t.Context(), FollowRequest{})
	if err != nil {
		t.Fatal(err)
	}
	p.AcceptBusiness([]Message{{Role: "notice", Content: "first"}, {Role: "notice", Content: "second"}}, 1, "turn", false)
	cut := snapshot(t, p)
	if len(cut.Records) != 2 || cut.Records[0].ID == "" || cut.Records[0].ID == cut.Records[1].ID {
		t.Fatalf("business identity allocation = %+v", cut.Records)
	}
	suffix := followChanges(t, p, FollowRequest{Subscription: initial.Subscription, AfterRevision: initial.Snapshot.ProjectionRevision})
	if len(suffix.Changes) != 1 || len(suffix.Changes[0].Records) != 2 || suffix.Changes[0].Records[0].RecordID == "" ||
		suffix.Changes[0].Records[0].RecordID == suffix.Changes[0].Records[1].RecordID {
		t.Fatalf("published business identities = %+v", suffix.Changes)
	}
}

func TestBusinessPublisherReleasesCompletedActiveRowsWithinHistoryBudget(t *testing.T) {
	p, err := NewProjection(testIdentity, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	businessFrame(t, p, 0, event.Event{Kind: event.StreamAttempt, MessageID: "old-answer", AttemptID: "old-answer", StreamAttempt: event.StreamAttemptInfo{ID: "old-answer", Action: event.StreamAttemptBegin}})
	businessFrame(t, p, 0, event.Event{Kind: event.Text, MessageID: "old-answer", Text: "old partial answer"})
	for i := range 120 {
		id := fmt.Sprintf("new-answer-%03d", i)
		p.AcceptBusiness([]Message{{RecordID: "m:" + id, MessageID: id, Role: "assistant", Content: id}}, uint64(i+1), fmt.Sprintf("turn-%d", i), false)
	}
	active := snapshot(t, p)
	retained := false
	for _, records := range [][]Record{active.Records, active.ActiveRecords} {
		for _, record := range records {
			retained = retained || record.Message.MessageID == "old-answer" && record.Message.Content == "old partial answer"
		}
	}
	if len(active.ActiveAttempts) != 1 || !retained {
		t.Fatal("appended history evicted the active assistant prefix")
	}
	// A terminal turn must release pending state even if its attempt end frame
	// was unavailable. No later user submission should be needed to reclaim
	// rows that were exempted from the resident budget while still active.
	businessFrame(t, p, 120, event.Event{Kind: event.TurnDone})
	cut := snapshot(t, p)
	if cut.TotalRecords > 96 || len(cut.ActiveRecords) != 0 || len(cut.ActiveAttempts) != 0 {
		t.Fatalf("completed rows escaped resident budget: total=%d activeRows=%d activeAttempts=%d", cut.TotalRecords, len(cut.ActiveRecords), len(cut.ActiveAttempts))
	}
	for _, record := range cut.Records {
		if record.Message.MessageID == "old-answer" || record.Message.Pending {
			t.Fatalf("completed active row was retained indefinitely: %+v", record.Message)
		}
	}
}
