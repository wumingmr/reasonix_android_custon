package agent

import (
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/tool"
	"reflect"
	"testing"
)

func TestResultProjectionDoesNotRewriteEvidenceOrHistory(t *testing.T) {
	a := New(nil, tool.NewRegistry(), NewSession("sys"), Options{}, event.Discard)
	a.task.ledger.Record(evidence.Receipt{ToolName: "edit_file", Success: true, Write: true, Paths: []string{"auth.go"}})
	messages := a.Session().Snapshot()
	receipts := a.task.ledger.Receipts()
	a.emitTurnShadows("complete work")
	if !reflect.DeepEqual(messages, a.Session().Snapshot()) || !reflect.DeepEqual(receipts, a.task.ledger.Receipts()) {
		t.Fatal("result projection rewrote source records")
	}
	if a.CompletionReceipt().Verdict != "unknown" {
		t.Fatal("host quality verdict created")
	}
}
