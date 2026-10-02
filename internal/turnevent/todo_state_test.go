package turnevent

import (
	"errors"
	"os"
	"testing"

	"reasonix/internal/event"
)

func TestTodoStateIsCommittedWithToolResultAndClearedByCommittedStart(t *testing.T) {
	path := testSessionPath(t)
	ledger, err := Open(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := ledger.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ledger.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("append start: ok=%v err=%v", ok, err)
	}
	todos := []event.Todo{{Content: "inspect", Status: "completed"}, {Content: "ship", Status: "in_progress"}}
	if _, ok, err := ledger.Append(event.Event{Kind: event.ToolResult, Tool: event.Tool{ID: "todo-1", Name: "todo_write", TodoWritten: true, Todos: todos}}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("append todo result: ok=%v err=%v", ok, err)
	}
	if got, written := ledger.TodoState(); !written || len(got) != 2 || got[1] != todos[1] {
		t.Fatalf("live todo state = %+v written=%v", got, written)
	}
	if _, ok, err := ledger.Append(event.Event{Kind: event.TurnDone}, event.TurnCompleted); err != nil || !ok {
		t.Fatalf("append terminal: ok=%v err=%v", ok, err)
	}
	if err := ledger.AcknowledgeProjection(turnID); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Compact(); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, written := reopened.TodoState(); !written || len(got) != 2 || got[0] != todos[0] {
		t.Fatalf("reopened todo state = %+v written=%v", got, written)
	}
	if _, err := reopened.Begin(); err != nil {
		t.Fatal(err)
	}
	if got, written := reopened.TodoState(); !written || len(got) != 2 {
		t.Fatalf("turn allocation changed todo state before commit = %+v written=%v", got, written)
	}
	if _, ok, err := reopened.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("append new start: ok=%v err=%v", ok, err)
	}
	if got, written := reopened.TodoState(); written || len(got) != 0 {
		t.Fatalf("new turn retained todo state = %+v written=%v", got, written)
	}
}

func TestFailedTurnStartCommitKeepsPreviousTodoState(t *testing.T) {
	ledger, err := Open("", "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Begin(); err != nil {
		t.Fatal(err)
	}
	_, _, _ = ledger.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress)
	want := []event.Todo{{Content: "previous", Status: "completed"}}
	_, _, _ = ledger.Append(event.Event{Kind: event.ToolResult, Tool: event.Tool{Name: "todo_write", TodoWritten: true, Todos: want}}, event.TurnInProgress)
	_, _, _ = ledger.Append(event.Event{Kind: event.TurnDone}, event.TurnCompleted)

	if _, err := ledger.Begin(); err != nil {
		t.Fatal(err)
	}
	// Force the first write attempt to fail before it can publish the new-turn
	// projection. A directory cannot be opened as the append-only ledger file.
	ledger.path = t.TempDir()
	if _, _, err := ledger.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress); err == nil || !errors.Is(err, ErrTurnLedgerUnavailable) {
		t.Fatalf("failed start append error = %v", err)
	}
	if got, written := ledger.TodoState(); !written || len(got) != 1 || got[0] != want[0] {
		t.Fatalf("failed start commit changed todo state = %+v written=%v", got, written)
	}
	if ledger.writer != nil {
		t.Fatal("failed start retained a writer")
	}
	if _, err := os.Stat(ledger.path); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitEmptyTodoStateSurvivesReplay(t *testing.T) {
	path := testSessionPath(t)
	ledger, err := Open(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Begin(); err != nil {
		t.Fatal(err)
	}
	_, _, _ = ledger.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress)
	_, _, _ = ledger.Append(event.Event{Kind: event.ToolResult, Tool: event.Tool{Name: "todo_write", TodoWritten: true, Todos: []event.Todo{}}}, event.TurnInProgress)
	_, _, _ = ledger.Append(event.Event{Kind: event.TurnDone}, event.TurnCompleted)
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, written := reopened.TodoState(); !written || len(got) != 0 {
		t.Fatalf("empty todo state = %+v written=%v", got, written)
	}
}

func TestRecoveryStatusPreservesExactDurableReason(t *testing.T) {
	path := testSessionPath(t)
	ledger, err := Open(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Begin(); err != nil {
		t.Fatal(err)
	}
	want := &event.RecoveryStatus{State: "recovery_required", Reason: "cancellation_grace_expired", Phase: "tool_execution", RequiresUserDecision: true}
	if _, ok, err := ledger.Append(event.Event{Kind: event.TurnDone, Recovery: want}, event.TurnRecoveryRequired); err != nil || !ok {
		t.Fatalf("append recovery = (%v, %v)", ok, err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got := reopened.RecoveryStatus()
	if got == nil || got.Reason != want.Reason || got.Phase != want.Phase || !got.RequiresUserDecision {
		t.Fatalf("recovery = %#v, want %#v", got, want)
	}
}
