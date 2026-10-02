package agent

import (
	"reasonix/internal/completion"
	"reasonix/internal/evidence"
	"testing"
)

func TestCompletionProjectionUsesFactsAndUnknownVerdict(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{ToolName: "edit_file", Mutation: true, Write: true, Success: true, Paths: []string{"calc.py"}})
	receipt := completionReceipt(completion.BuildFacts(ledger, "", nil))
	if receipt == nil || receipt.AssessmentKind != "facts" || receipt.Verdict != "unknown" || len(receipt.Gaps) != 0 {
		t.Fatalf("receipt=%+v", receipt)
	}
}
