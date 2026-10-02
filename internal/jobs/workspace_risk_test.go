package jobs

import (
	"testing"

	"reasonix/internal/evidence"
)

func TestTaskMutationEvidencePreservesPathsWithoutRisk(t *testing.T) {
	summary := evidence.ChildEvidenceSummary{
		WorkspaceRoot: "/workspace/toolbox",
		Receipts: []evidence.Receipt{{
			ToolName: "edit_file",
			Success:  true,
			Write:    true,
			Mutation: true,
			Paths:    []string{"/workspace/toolbox/internal/agent/worker.go"},
		}},
	}
	meta := mutationEvidenceForArtifact(summary)
	if meta == nil || meta.Risk != "" || len(meta.Paths) != 1 || meta.Paths[0] != summary.Receipts[0].Paths[0] {
		t.Fatalf("ordinary workspace mutation evidence = %+v, want paths without risk", meta)
	}

	summary.Receipts[0].Paths = []string{"/workspace/toolbox/internal/auth/session.go"}
	meta = mutationEvidenceForArtifact(summary)
	if meta == nil || meta.Risk != "" || len(meta.Paths) != 1 || meta.Paths[0] != summary.Receipts[0].Paths[0] {
		t.Fatalf("sensitive workspace mutation evidence = %+v, want paths without risk", meta)
	}
}
