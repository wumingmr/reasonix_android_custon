package completion

import (
	"fmt"
	"strings"

	"reasonix/internal/evidence"
)

// Verdict is the report's headline. Partial is terminal: the work is proven
// against its criteria, and the gaps it still carries are declared rather
// than hidden.
type Verdict uint8

const (
	VerdictUnknown Verdict = iota
	VerdictIncomplete
	VerdictPartial
	VerdictDone
)

func (v Verdict) String() string {
	switch v {
	case VerdictIncomplete:
		return "incomplete"
	case VerdictPartial:
		return "partial"
	case VerdictDone:
		return "done"
	default:
		return "unknown"
	}
}

// Change is one path the turn mutated. Reviewed reports whether the changed
// result was inspected after the last write to it.
type Change struct {
	Path     string
	Reviewed bool
}

// Verification is a delivery-verification command's latest outcome. Stale
// means it last ran before the newest mutation, so it proves nothing about
// the current tree.
type Verification struct {
	Command     string
	Passed      bool
	Stale       bool
	ToolCallID  string
	Interrupted bool
	ExitCode    *int
}

// GapKind classifies one thing the report refuses to present as verified.
type GapKind uint8

const (
	// GapUnbackedClaim is first because it is the worst: the turn asserted a
	// verification the ledger does not support.
	GapUnbackedClaim GapKind = iota
	GapUnprovenCriterion
	GapMissingCheck
	GapFailedVerification
	GapStaleVerification
	GapUnverifiedChange
	GapUnreviewedChange
	GapDeclaredUnverified
)

func (k GapKind) String() string {
	switch k {
	case GapUnbackedClaim:
		return "unbacked_claim"
	case GapDeclaredUnverified:
		return "declared_unverified"
	case GapUnprovenCriterion:
		return "unproven_criterion"
	case GapMissingCheck:
		return "missing_check"
	case GapFailedVerification:
		return "failed_verification"
	case GapStaleVerification:
		return "stale_verification"
	case GapUnverifiedChange:
		return "unverified_change"
	case GapUnreviewedChange:
		return "unreviewed_change"
	default:
		return "unknown"
	}
}

// Gap is one unproven thing, in the report's own words.
type Gap struct {
	Kind   GapKind
	Detail string
}

// Report is the host's completion record for one turn.
type Report struct {
	// AssessmentKind distinguishes observed facts from historical quality assessments.
	AssessmentKind string
	Verdict        Verdict
	// Mutations counts every successful mutating receipt, including ones that
	// named no path; Changes lists only the paths.
	Mutations     int
	Changes       []Change
	Verifications []Verification
	Gaps          []Gap
	// Claimed is what the turn said about itself; Risks is its declared risk
	// list. Both are model-authored and never clear a host-found gap.
	Claimed Claim
	Risks   []string
}

// BuildFacts records observations and model declarations without assigning a
// quality verdict or inventing checks that were never run.
func BuildFacts(ledger *evidence.Ledger, workspaceRoot string, scratchRoots []string) Report {
	receipts := ledger.Receipts()
	rep := Report{
		AssessmentKind: "facts",
		Verdict:        VerdictUnknown,
		Mutations:      mutationsOf(receipts, workspaceRoot, scratchRoots),
		Changes:        changesOf(ledger, receipts, workspaceRoot, scratchRoots),
		Verifications:  verificationsOf(receipts, workspaceRoot, scratchRoots),
	}
	return reconcile(rep, claimOf(receipts), receipts)
}

// changesOf lists mutated paths in first-write order and asks the ledger
// whether each one was inspected after its own latest write, so a review that
// covered one file never vouches for another.
func changesOf(ledger *evidence.Ledger, receipts []evidence.Receipt, workspaceRoot string, scratchRoots []string) []Change {
	var out []Change
	at := map[string]int{}
	lastWrite := map[string]int{}
	for i, r := range receipts {
		if !evidence.IsDeliveryMutation(r, workspaceRoot, scratchRoots) {
			continue
		}
		for _, p := range r.Paths {
			if p == "" || evidence.ClassifyWriteScope(p, workspaceRoot, scratchRoots) == evidence.WriteScopeScratch {
				continue
			}
			if _, seen := at[p]; !seen {
				at[p] = len(out)
				out = append(out, Change{Path: p})
			}
			lastWrite[p] = i
		}
	}
	for i := range out {
		out[i].Reviewed = ledger.HasHostReviewCoverageAfter(lastWrite[out[i].Path], []string{out[i].Path})
	}
	return out
}

// mutationsOf counts successful mutating receipts, path-named or not: a
// `sed -i` or `rm` that named nothing still changed the workspace, and must
// not escape the unverified-change gap by leaving no path behind.
func mutationsOf(receipts []evidence.Receipt, workspaceRoot string, scratchRoots []string) int {
	count := 0
	for _, r := range receipts {
		if evidence.IsDeliveryMutation(r, workspaceRoot, scratchRoots) {
			count++
		}
	}
	return count
}

// verificationsOf keeps each delivery-verification command's latest run, in
// first-run order, and marks the ones that predate the newest mutation.
func verificationsOf(receipts []evidence.Receipt, workspaceRoot string, scratchRoots []string) []Verification {
	lastMutation := -1
	for i, r := range receipts {
		if evidence.IsDeliveryMutation(r, workspaceRoot, scratchRoots) {
			lastMutation = i
		}
	}
	var out []Verification
	at := map[string]int{}
	for i, r := range receipts {
		command := strings.TrimSpace(r.Command)
		if command == "" || r.Verification == evidence.VerificationNotVerification || r.Verification == evidence.VerificationNotRun || !evidence.IsVerificationCommand(command) {
			continue
		}
		if _, seen := at[command]; !seen {
			at[command] = len(out)
			out = append(out, Verification{Command: command})
		}
		out[at[command]].Passed = r.Success && (r.ExitCode == nil || *r.ExitCode == 0) && r.Verification != evidence.VerificationFailed
		out[at[command]].Stale = i < lastMutation
		out[at[command]].ToolCallID = r.ToolCallID
		out[at[command]].Interrupted = r.Interrupted
		out[at[command]].ExitCode = r.ExitCode
	}
	return out
}

// Summary describes observed quantities without assigning quality.
func (r Report) Summary() string {
	return fmt.Sprintf("%d changes, %d checks", len(r.Changes), len(r.Verifications))
}

func (r Report) GapKinds() []string {
	seen := map[GapKind]bool{}
	var out []string
	for _, kind := range []GapKind{GapUnbackedClaim, GapUnprovenCriterion, GapMissingCheck, GapFailedVerification, GapStaleVerification, GapUnverifiedChange, GapUnreviewedChange, GapDeclaredUnverified} {
		for _, gap := range r.Gaps {
			if gap.Kind == kind && !seen[kind] {
				seen[kind] = true
				out = append(out, kind.String())
			}
		}
	}
	return out
}
