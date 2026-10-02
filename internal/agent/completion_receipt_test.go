package agent

import (
	"testing"

	"reasonix/internal/completion"
	"reasonix/internal/evidence"
)

func TestReceiptCarriesWhatProseDoesNot(t *testing.T) {
	ledger := evidence.NewLedger()
	for _, r := range []evidence.Receipt{
		{ToolName: "edit_file", Success: true, Write: true, Mutation: true, Paths: []string{"calc.py"}},
		{ToolName: "bash", Success: true, Command: "go test ./...", OutputBytes: 64},
	} {
		ledger.Record(r)
	}
	got := completionReceipt(completion.BuildFacts(ledger, "", nil))
	if got == nil {
		t.Fatal("a turn that changed a file must produce a receipt")
	}
	if got.Verdict != "unknown" {
		t.Fatalf("verdict = %q, want unknown", got.Verdict)
	}
	if len(got.Changes) != 1 || got.Changes[0].Path != "calc.py" || got.Changes[0].Reviewed {
		t.Fatalf("changes = %+v, want calc.py recorded as unreviewed", got.Changes)
	}
	if len(got.Verifications) != 1 || !got.Verifications[0].Passed {
		t.Fatalf("verifications = %+v, want the passing command named", got.Verifications)
	}
	if len(got.Gaps) != 0 {
		t.Fatalf("unreviewed path acquired inferred quality gap: %+v", got.Gaps)
	}
}

// A receipt that says nothing is noise, and noise is what makes people stop
// reading receipts.
func TestNoReceiptForATurnWithNothingToJudge(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{ToolName: "read_file", Success: true, Read: true, Paths: []string{"calc.py"}, OutputBytes: 64})
	if got := completionReceipt(completion.BuildFacts(ledger, "", nil)); got != nil {
		t.Fatalf("read-only answer produced a receipt: %+v", got)
	}
}

func TestAgentHoldsTheLastTurnsReceipt(t *testing.T) {
	var a *Agent
	if a.CompletionReceipt() != nil {
		t.Fatal("a nil agent must not produce a receipt")
	}
	a = &Agent{}
	if a.CompletionReceipt() != nil {
		t.Fatal("an agent that has not finished a turn has no receipt")
	}
}
