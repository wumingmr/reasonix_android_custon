package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

type unavailableTool struct {
	fakeTool
	calls *int32
}

func (u unavailableTool) ProviderVisible(context.Context) bool { return false }

func (u unavailableTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if u.calls != nil {
		atomic.AddInt32(u.calls, 1)
	}
	return u.fakeTool.Execute(ctx, args)
}

func TestContextualToolHostGateRunsBeforePermissionAndExecute(t *testing.T) {
	var executions int32
	reg := tool.NewRegistry()
	reg.Add(unavailableTool{fakeTool: fakeTool{name: "phase_tool", readOnly: false}, calls: &executions})
	gate := &recordingPermissionGate{allow: true}
	a := New(nil, reg, NewSession("sys"), Options{Gate: gate}, event.Discard)

	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{ID: "phase", Name: "phase_tool", Arguments: `{}`})
	if !out.blocked || !strings.Contains(out.output, "unavailable") {
		t.Fatalf("contextual tool outcome = %+v", out)
	}
	if executions != 0 || len(gate.calls) != 0 {
		t.Fatalf("unavailable tool crossed host gate: executions=%d permission=%+v", executions, gate.calls)
	}
}

func TestMixedContextualBatchExecutesAvailableCallsOnce(t *testing.T) {
	var executions int32
	reg := tool.NewRegistry()
	reg.Add(unavailableTool{fakeTool: fakeTool{name: "phase_tool", readOnly: true}, calls: &executions})
	reg.Add(fakeTool{name: "read_file", readOnly: true, calls: &executions})
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{toolCallChunk("phase", "phase_tool", `{}`), toolCallChunk("read", "read_file", `{}`), {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "answer after the available read"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, reg, NewSession("sys"), Options{}, event.Discard)
	if err := a.Run(context.Background(), "inspect the file"); err != nil {
		t.Fatalf("mixed contextual batch failed: %v", err)
	}
	if got := atomic.LoadInt32(&executions); got != 1 {
		t.Fatalf("available tool executions = %d, want exactly one", got)
	}
	if got := lastToolResult(a.Session(), "phase_tool"); !strings.Contains(got, "unavailable") {
		t.Fatalf("contextual result = %q", got)
	}
}

func TestRepeatedMixedContextualBatchKeepsIndependentCallsRunning(t *testing.T) {
	var executions int32
	reg := tool.NewRegistry()
	reg.Add(unavailableTool{fakeTool: fakeTool{name: "phase_tool", readOnly: true}, calls: &executions})
	reg.Add(fakeTool{name: "read_file", readOnly: true, calls: &executions})
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{toolCallChunk("phase-1", "phase_tool", `{}`), toolCallChunk("read-1", "read_file", `{}`), {Type: provider.ChunkDone}},
		{toolCallChunk("phase-2", "phase_tool", `{}`), toolCallChunk("read-2", "read_file", `{}`), {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "answer after repeated local failures"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, reg, NewSession("sys"), Options{}, event.Discard)
	if err := a.Run(context.Background(), "inspect the file"); err != nil {
		t.Fatalf("repeated contextual batch failed: %v", err)
	}
	if got := atomic.LoadInt32(&executions); got != 2 {
		t.Fatalf("available tool executions = %d, want both independent calls", got)
	}
	if got := lastToolResult(a.Session(), "phase_tool"); !strings.Contains(got, "unavailable") {
		t.Fatalf("second unavailable call result = %q", got)
	}
	if prov.call != 3 {
		t.Fatalf("provider calls = %d, want a normal final round", prov.call)
	}
}

func TestRepeatedPureContextualCallWithAnswerRemainsRecoverable(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(unavailableTool{fakeTool: fakeTool{name: "phase_tool", readOnly: true}})
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{toolCallChunk("phase-1", "phase_tool", `{}`), {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "占位 Lorem 占位 ipsum — the request is fully handled."}, toolCallChunk("phase-2", "phase_tool", `{}`), {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "final answer"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, reg, NewSession("sys"), Options{}, event.Discard)
	if err := a.Run(context.Background(), "answer normally"); err != nil {
		t.Fatalf("repeated contextual call failed: %v", err)
	}
	if prov.call != 3 {
		t.Fatalf("provider calls = %d, want a normal final round", prov.call)
	}
	if got := lastToolResult(a.Session(), "phase_tool"); !strings.Contains(got, "unavailable") {
		t.Fatalf("second unavailable call result = %q", got)
	}
}
