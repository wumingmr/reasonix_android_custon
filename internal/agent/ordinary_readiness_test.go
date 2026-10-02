package agent

import (
	"context"
	"testing"

	"reasonix/internal/instruction"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func TestTargetedTurnReturnsIncompleteReceiptInsteadOfReadinessRecovery(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "write_file", readOnly: false})
	reg.Add(fakeTool{name: "bash", readOnly: false})
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{
			toolCallChunk("c1", "write_file", `{"path":"changed.go","content":"package main"}`),
			{Type: provider.ChunkDone},
		},
		{{Type: provider.ChunkText, Text: "changed, verification not run"}, {Type: provider.ChunkDone}},
	}}
	sink := &phaseSink{}
	a := New(prov, reg, NewSession(""), Options{
		ProjectChecks: []instruction.VerifyCheck{{Command: "go test ./...", SourcePath: "AGENTS.md", Line: 3}},
	}, sink)

	if err := a.Run(withNoClosedLoop(context.Background()), "update changed.go"); err != nil {
		t.Fatalf("targeted Run returned a readiness recovery: %v", err)
	}
	receipt := a.CompletionReceipt()
	if receipt == nil || receipt.Verdict != "unknown" {
		t.Fatalf("completion receipt = %+v, want incomplete", receipt)
	}
	if len(receipt.Gaps) != 0 || len(sink.summaries) != 0 {
		t.Fatalf("host invented requirements: %+v summaries=%+v", receipt.Gaps, sink.summaries)
	}
}
