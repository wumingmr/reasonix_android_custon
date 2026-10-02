package completion

import (
	"path/filepath"
	"reasonix/internal/evidence"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func ledgerOf(receipts ...evidence.Receipt) *evidence.Ledger {
	l := evidence.NewLedger()
	for _, r := range receipts {
		l.Record(r)
	}
	return l
}

func wrote(path string) evidence.Receipt {
	return evidence.Receipt{ToolName: "write_file", Success: true, Write: true, Mutation: true, Paths: []string{path}}
}

func read(path string) evidence.Receipt {
	return evidence.Receipt{ToolName: "read_file", Success: true, Read: true, Paths: []string{path}, OutputBytes: 64}
}

func ran(command string, ok bool) evidence.Receipt {
	return evidence.Receipt{ToolName: "bash", Success: ok, Command: command, OutputBytes: 64}
}

func gapKinds(rep Report) []string { return rep.GapKinds() }

func TestFactsPreserveFailedAndStaleChecksWithoutQualityVerdict(t *testing.T) {
	for _, success := range []bool{false, true} {
		for _, laterWrite := range []bool{false, true} {
			ledger := ledgerOf(wrote("parser.go"), ran("go test ./...", success))
			if laterWrite {
				ledger.Record(wrote("auth/session.go"))
			}
			before := ledger.Receipts()
			report := BuildFacts(ledger, "", nil)
			if report.AssessmentKind != "facts" || report.Verdict != VerdictUnknown || len(report.Verifications) != 1 {
				t.Fatalf("report=%+v", report)
			}
			check := report.Verifications[0]
			if check.Passed != success || check.Stale != laterWrite {
				t.Fatalf("check=%+v", check)
			}
			if len(report.Gaps) != 0 || !reflect.DeepEqual(before, ledger.Receipts()) {
				t.Fatal("facts created inferred obligations or rewrote evidence")
			}
		}
	}
}
func TestFactsDoNotInventMissingChecksOrReviews(t *testing.T) {
	report := BuildFacts(ledgerOf(wrote("internal/auth/session.go"), wrote("schema/migration.sql")), "", nil)
	if report.Verdict != VerdictUnknown || len(report.Verifications) != 0 || len(report.Gaps) != 0 || len(report.Changes) != 2 {
		t.Fatalf("report=%+v", report)
	}
}
func TestFactsExcludeScratchChanges(t *testing.T) {
	project, scratch := t.TempDir(), t.TempDir()
	path := filepath.Join(project, "main.go")
	report := BuildFacts(ledgerOf(wrote(path), wrote(filepath.Join(scratch, "temp.go"))), project, []string{scratch})
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	if len(report.Changes) != 1 || report.Changes[0].Path != path {
		t.Fatalf("changes=%+v", report.Changes)
	}
}
