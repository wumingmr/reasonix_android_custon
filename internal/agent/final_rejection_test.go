package agent

import (
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/tool"
	"reflect"
	"testing"
)

func TestPlanAcceptanceIsNotAHostCompletionCondition(t *testing.T) {
	a := New(nil, tool.NewRegistry(), NewSession(""), Options{}, event.Discard)
	plan := criterionPlan()
	a.SetPlanContract(&plan)
	a.task.ledger.Record(evidence.Receipt{ToolName: "bash", Command: "go test ./...", Success: false})
	before := a.task.ledger.Receipts()
	if !a.ReadinessResult().Ready || !reflect.DeepEqual(before, a.task.ledger.Receipts()) {
		t.Fatal("acceptance text enforced or changed facts")
	}
}
