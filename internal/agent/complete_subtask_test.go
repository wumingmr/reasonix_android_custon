package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func submitCompleteSubtask(t *testing.T, led *evidence.Ledger, args string) string {
	t.Helper()
	ctx := evidence.WithLedger(context.Background(), led)
	out, err := NewCompleteSubtaskTool().Execute(ctx, json.RawMessage(args))
	if err != nil {
		t.Fatalf("complete_subtask: %v", err)
	}
	return out
}

// The model's report is preserved; execution facts are a separate projection.
func TestCompleteSubtaskPreservesModelReport(t *testing.T) {
	led := evidence.NewLedger()
	led.Record(evidence.Receipt{ToolName: "bash", Command: "go test ./parser", Success: true, OutputBytes: 12})

	args := `{
		"status":"complete",
		"summary":"fixed the parser",
		"acceptance_criteria":[
			{"id":"AC1","status":"satisfied","evidence":[{"kind":"verification","summary":"unit tests","command":"go test ./parser"}]},
			{"id":"AC2","status":"satisfied","evidence":[{"kind":"verification","summary":"integration suite","command":"go test ./integration"}]}
		]}`
	if out := submitCompleteSubtask(t, led, args); !strings.Contains(out, "status=complete") {
		t.Fatalf("tool result = %q, want the model's status", out)
	}

	// The agent host, not the tool, records the call; replay that receipt so the
	// ledger lookup the report renderer uses is covered too.
	led.Record(evidence.ReceiptFromToolCall("complete_subtask", json.RawMessage(args), true, true))
	report, ok := led.LatestCompletionReport()
	if !ok {
		t.Fatal("a recorded complete_subtask call must be recoverable from the ledger")
	}
	if report.Status != evidence.CompletionComplete || report.Criteria[1].Status != evidence.CriterionSatisfied {
		t.Fatalf("host rewrote the model report: %+v", report)
	}
}

func TestCompleteSubtaskKeepsFullyBackedClaim(t *testing.T) {
	led := evidence.NewLedger()
	led.Record(evidence.Receipt{ToolName: "bash", Command: "go test ./parser", Success: true, OutputBytes: 12})
	led.Record(evidence.Receipt{ToolName: "write_file", Success: true, Mutation: true, Write: true, Paths: []string{"parser.go"}})

	out := submitCompleteSubtask(t, led, `{
		"status":"complete",
		"summary":"fixed the parser",
		"acceptance_criteria":[
			{"id":"AC1","status":"satisfied","evidence":[{"kind":"verification","summary":"tests","command":"go test ./parser"}]},
			{"id":"AC2","status":"satisfied","evidence":[{"kind":"diff","summary":"the fix","paths":["parser.go"]}]}
		]}`)
	if !strings.Contains(out, "status=complete") || strings.Contains(out, "lowered") {
		t.Fatalf("tool result = %q, want an untouched complete status", out)
	}
}

func TestCompleteSubtaskAcceptsReportWithoutHostEvidence(t *testing.T) {
	out, err := NewCompleteSubtaskTool().Execute(context.Background(), json.RawMessage(`{
		"status":"complete","summary":"done",
		"acceptance_criteria":[
			{"id":"AC1","status":"satisfied","evidence":[{"kind":"manual","summary":"I checked it"}]},
			{"id":"AC2","status":"satisfied"}
		]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "status=complete") || strings.Contains(out, "lowered") {
		t.Fatalf("report was adjudicated: %s", out)
	}
}

func TestParseCompletionReportRejectsMalformedClaims(t *testing.T) {
	for name, args := range map[string]string{
		"bad status":           `{"status":"done","summary":"x"}`,
		"missing summary":      `{"status":"complete"}`,
		"verification no cmd":  `{"status":"complete","summary":"x","acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":[{"kind":"verification","summary":"tests"}]}]}`,
		"diff without paths":   `{"status":"complete","summary":"x","acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":[{"kind":"diff","summary":"change"}]}]}`,
		"unknown evidence":     `{"status":"complete","summary":"x","acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":[{"kind":"vibes","summary":"trust me"}]}]}`,
		"criterion without id": `{"status":"complete","summary":"x","acceptance_criteria":[{"status":"satisfied"}]}`,
	} {
		if _, err := evidence.ParseCompletionReport(json.RawMessage(args)); err == nil {
			t.Errorf("%s: accepted a malformed report", name)
		}
	}
}

func TestSubAgentAnswerSeparatesModelReportFromExecutionFacts(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeWriteFileTool{})
	AttachCompleteSubtaskTool(reg)
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{toolCallChunk("1", "write_file", `{"path":"parser.go"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("2", "complete_subtask", `{"status":"complete","summary":"fixed the parser","acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":[{"kind":"diff","summary":"the fix","paths":["parser.go"]}]},{"id":"AC2","status":"satisfied","evidence":[{"kind":"verification","summary":"suite","command":"go test ./..."}]}],"unresolved":["integration suite not executed"]}`), {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "all good"}, {Type: provider.ChunkDone}},
	}}

	answer, err := RunSubAgentWithSession(withNoClosedLoop(context.Background()), prov, reg, NewSession("sys"),
		"fix the parser", Options{}, event.Discard)
	if err != nil {
		t.Fatalf("RunSubAgentWithSession: %v", err)
	}
	if !strings.HasPrefix(answer, "Model-reported status: complete") {
		t.Fatalf("answer must label the model's assessment:\n%s", answer)
	}
	for _, want := range []string{
		"AC1 satisfied",
		"AC2 satisfied",
		"unresolved: integration suite not executed",
		hostReceiptsHeader,
	} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer missing %q:\n%s", want, answer)
		}
	}
	if strings.Contains(answer, "host lowered") {
		t.Fatalf("host adjudication survived: %s", answer)
	}
}

func TestSubAgentMayFinishWithoutCompletionReport(t *testing.T) {
	reg := tool.NewRegistry()
	AttachCompleteSubtaskTool(reg)
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{{Type: provider.ChunkText, Text: "Analysis complete."}, {Type: provider.ChunkDone}},
	}}
	answer, err := RunSubAgentWithSession(context.Background(), prov, reg, NewSession("sys"),
		completeSubtaskContract, Options{}, event.Discard)
	if err != nil || answer != "Analysis complete." {
		t.Fatalf("plain completion = %q, %v", answer, err)
	}
}
