package agent

import (
	"encoding/json"
	"reasonix/internal/completion"
	"reasonix/internal/evidence"
	"reasonix/internal/plancontract"
	"testing"
)

func completeStepReceipt(t *testing.T, criterionID, kind, command string) evidence.Receipt {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"step":   "do it",
		"result": "done",
		"evidence": []map[string]any{
			{"kind": kind, "summary": "proved it", "command": command, "criterion_id": criterionID},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return evidence.Receipt{ToolName: "complete_step", Success: true, Step: "do it", Args: args}
}

func criterionPlan() plancontract.Plan {
	return plancontract.Plan{
		Objective: "fix the retry race",
		Steps: []plancontract.Step{{
			Title: "fix it",
			Acceptance: []plancontract.Criterion{
				{Text: "retries no longer double-charge"},
				{Text: "existing payment tests keep passing", Regression: true},
			},
		}},
	}.Normalize()
}

func TestCriterionClaimDoesNotCertifyFailedExecution(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{ToolName: "bash", Command: "go test ./...", Success: false})
	ledger.Record(completeStepReceipt(t, "c1", "verification", "go test ./..."))
	report := completion.BuildFacts(ledger, "", nil)
	if report.Verdict != completion.VerdictUnknown || len(report.Verifications) != 1 || report.Verifications[0].Passed {
		t.Fatalf("claim altered facts: %+v", report)
	}
}
