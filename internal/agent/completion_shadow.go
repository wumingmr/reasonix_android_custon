package agent

import (
	"reasonix/internal/completion"
	"reasonix/internal/event"
)

// completionReceipt projects the report onto the delivery shape. Runs with
// nothing to judge return nil rather than an empty card: a receipt that says
// nothing is noise, and noise is what makes people stop reading receipts.
func completionReceipt(rep completion.Report) *event.CompletionReceipt {
	if rep.Verdict == completion.VerdictUnknown && rep.Mutations == 0 && len(rep.Verifications) == 0 && len(rep.Gaps) == 0 && len(rep.Risks) == 0 {
		return nil
	}
	out := &event.CompletionReceipt{AssessmentKind: rep.AssessmentKind, Verdict: rep.Verdict.String(), Risks: rep.Risks}
	for _, change := range rep.Changes {
		out.Changes = append(out.Changes, event.ReceiptChange{Path: change.Path, Reviewed: change.Reviewed})
	}
	for _, v := range rep.Verifications {
		out.Verifications = append(out.Verifications, event.ReceiptVerification{Command: v.Command, Passed: v.Passed, Stale: v.Stale, ToolCallID: v.ToolCallID, ExitCode: v.ExitCode, Interrupted: v.Interrupted})
	}
	for _, gap := range rep.Gaps {
		out.Gaps = append(out.Gaps, event.ReceiptGap{Kind: gap.Kind.String(), Detail: gap.Detail})
	}
	return out
}
