package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/fileops"
	"reasonix/internal/provider"
	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
	"reasonix/internal/tool/builtin"
)

func TestSameBatchReadEditBashEditUsesExecutionOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "中文.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\r\ngamma\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	for _, target := range (builtin.Workspace{Dir: dir, Bash: sandbox.Spec{Mode: "off"}}).Tools("read_file", "edit_file") {
		reg.Add(target)
	}
	var bashCalls int32
	reg.Add(fakeTool{name: "bash", calls: &bashCalls})
	a := New(nil, reg, NewSession(""), Options{}, event.Discard)

	calls := []provider.ToolCall{
		{ID: "r", Name: "read_file", Arguments: `{"path":"中文.txt","limit":1}`},
		{ID: "e1", Name: "edit_file", Arguments: `{"path":"中文.txt","old_string":"beta","new_string":"BETA"}`},
		{ID: "b", Name: "bash", Arguments: `{}`},
		{ID: "e2", Name: "edit_file", Arguments: `{"path":"中文.txt","old_string":"gamma","new_string":"GAMMA"}`},
	}
	a.Session().Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: calls})
	batch := a.executeBatch(context.Background(), &a.turn, calls)
	if batch.err != nil {
		t.Fatal(batch.err)
	}
	for i, out := range batch.outcomes {
		if out.errMsg != "" || out.blocked {
			t.Fatalf("call %d failed: %+v", i, out)
		}
	}
	if atomic.LoadInt32(&bashCalls) != 1 {
		t.Fatalf("bash calls = %d, want 1", bashCalls)
	}
	got, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(got), "BETA\r\nGAMMA") {
		t.Fatalf("file = %q, err=%v", got, err)
	}
}

func TestThreeReadEditCommandEditCyclesIncludingMoveAndDelete(t *testing.T) {
	dir := t.TempDir()
	reg := tool.NewRegistry()
	for _, target := range (builtin.Workspace{Dir: dir, Bash: sandbox.Spec{Mode: "off"}}).Tools(
		"read_file", "edit_file", "move_file", "delete_range",
	) {
		reg.Add(target)
	}
	var bashCalls int32
	reg.Add(fakeTool{name: "bash", calls: &bashCalls})
	a := New(nil, reg, NewSession(""), Options{}, event.Discard)

	names := []string{"循环-一.txt", "循环-二.txt", "循环-三.txt"}
	for i, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("header\r\nbeta\r\ngamma\r\ntail\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		calls := []provider.ToolCall{
			{ID: fmt.Sprintf("r%d", i), Name: "read_file", Arguments: `{"path":"` + name + `","limit":1}`},
			{ID: fmt.Sprintf("e%da", i), Name: "edit_file", Arguments: `{"path":"` + name + `","old_string":"beta","new_string":"BETA"}`},
			{ID: fmt.Sprintf("b%d", i), Name: "bash", Arguments: `{}`},
			{ID: fmt.Sprintf("e%db", i), Name: "edit_file", Arguments: `{"path":"` + name + `","old_string":"gamma","new_string":"GAMMA"}`},
		}
		a.Session().Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: calls})
		batch := a.executeBatch(context.Background(), &a.turn, calls)
		if batch.err != nil {
			t.Fatal(batch.err)
		}
		for n, outcome := range batch.outcomes {
			if outcome.errMsg != "" || outcome.blocked {
				t.Fatalf("cycle %d call %d failed: %+v", i, n, outcome)
			}
		}
	}

	post := []provider.ToolCall{
		{ID: "move", Name: "move_file", Arguments: `{"source_path":"循环-一.txt","destination_path":"已移动.txt"}`},
		{ID: "delete", Name: "delete_range", Arguments: `{"path":"循环-二.txt","start_anchor":"BETA","end_anchor":"GAMMA","inclusive":true}`},
	}
	a.Session().Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: post})
	batch := a.executeBatch(context.Background(), &a.turn, post)
	if batch.err != nil {
		t.Fatal(batch.err)
	}
	for i, outcome := range batch.outcomes {
		if outcome.errMsg != "" || outcome.blocked {
			t.Fatalf("post call %d failed: %+v", i, outcome)
		}
	}
	if atomic.LoadInt32(&bashCalls) != 3 {
		t.Fatalf("bash calls = %d, want 3", bashCalls)
	}
}

func TestObservationSurvivesOrdinaryTurnButIsAgentLocal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	for _, target := range (builtin.Workspace{Dir: dir}).Tools("read_file", "edit_file") {
		reg.Add(target)
	}
	parent := New(nil, reg, NewSession(""), Options{}, event.Discard)
	child := New(nil, reg, NewSession(""), Options{}, event.Discard)

	read := []provider.ToolCall{{ID: "read", Name: "read_file", Arguments: `{"path":"state.txt","limit":1}`}}
	parent.Session().Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: read})
	if batch := parent.executeBatch(context.Background(), &parent.turn, read); batch.err != nil || batch.outcomes[0].errMsg != "" {
		t.Fatalf("read failed: %+v", batch)
	}
	parent.turn = turnRuntime{} // ordinary user turn replaces only per-turn state
	edit := []provider.ToolCall{{ID: "edit", Name: "edit_file", Arguments: `{"path":"state.txt","old_string":"old","new_string":"parent"}`}}
	parent.Session().Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: edit})
	if batch := parent.executeBatch(context.Background(), &parent.turn, edit); batch.err != nil || batch.outcomes[0].errMsg != "" {
		t.Fatalf("cross-turn edit failed: %+v", batch)
	}

	childEdit := []provider.ToolCall{{ID: "child-edit", Name: "edit_file", Arguments: `{"path":"state.txt","old_string":"parent","new_string":"child"}`}}
	child.Session().Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: childEdit})
	batch := child.executeBatch(context.Background(), &child.turn, childEdit)
	if len(batch.outcomes) != 1 || !strings.Contains(batch.outcomes[0].errMsg, string(tool.FSNotObserved)) {
		t.Fatalf("child inherited parent observation: %+v", batch)
	}
}

func TestSessionReplacementClearsFileObservations(t *testing.T) {
	a := New(nil, tool.NewRegistry(), NewSession(""), Options{}, event.Discard)
	target := fileops.OverlayTarget("/workspace/a.txt")
	a.fileObservations.ObservePresent(target, fileops.OverlayVersion("old"))
	a.SetSession(NewSession(""))
	if got := a.fileObservations.Get(target); got.Kind != fileops.Unseen {
		t.Fatalf("observation crossed session replacement: %+v", got)
	}
}

func TestBoundedLargeReadDoesNotGateCommandOrFinalAnswer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "large.txt"), []byte(strings.Repeat("line\n", 100000)), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	for _, target := range (builtin.Workspace{Dir: dir}).Tools("read_file") {
		reg.Add(target)
	}
	var bashCalls int32
	reg.Add(fakeTool{name: "bash", calls: &bashCalls})
	prov := &scriptedProvider{name: "bounded-read", turns: [][]provider.Chunk{
		{toolCallChunk("r", "read_file", `{"path":"large.txt","limit":1}`), toolCallChunk("b", "bash", `{}`), {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "done after one useful window"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)
	if err := a.Run(context.Background(), "inspect one line, run a command, and finish"); err != nil {
		t.Fatal(err)
	}
	if prov.call != 2 || atomic.LoadInt32(&bashCalls) != 1 {
		t.Fatalf("provider calls=%d bash calls=%d", prov.call, bashCalls)
	}
}

func TestUnknownPriorEffectIsFactNotExecutionBarrier(t *testing.T) {
	var calls int32
	target := fakeTool{name: "external_write", calls: &calls}
	reg := tool.NewRegistry()
	reg.Add(target)
	record := provider.ToolCallRecord{
		Identity: provider.ActionIdentity{CallID: "old", AttemptID: "attempt-old", CanonicalTool: target.Name(), ArgumentDigest: recoveryDigest([]byte(`{}`)), ResourceScope: "session:fixture"},
		State:    provider.ToolRunUnknown, Arguments: json.RawMessage(`{}`),
	}
	sess := NewSession("")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "old request"})
	sess.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "old", Name: target.Name(), Arguments: `{}`, Recovery: &record}}})
	a := New(nil, reg, sess, Options{}, event.Discard)

	newCalls := []provider.ToolCall{{ID: "new", Name: target.Name(), Arguments: `{}`}}
	a.Session().Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: newCalls})
	batch := a.executeBatch(context.Background(), &a.turn, newCalls)
	if batch.err != nil || len(batch.outcomes) != 1 || batch.outcomes[0].errMsg != "" || batch.outcomes[0].blocked {
		t.Fatalf("new call was blocked by historical unknown: %+v", batch)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("new call executions = %d, want 1", calls)
	}
	if pending := a.PendingToolRecovery(); len(pending) != 1 || pending[0].Identity.AttemptID != "attempt-old" {
		t.Fatalf("historical fact changed: %+v", pending)
	}
}

func TestRetiredProofToolReturnsOrdinaryPairedResult(t *testing.T) {
	a := New(nil, tool.NewRegistry(), NewSession(""), Options{}, event.Discard)
	calls := []provider.ToolCall{{ID: "old", Name: "complete_step", Arguments: `{}`}}
	a.Session().Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: calls})
	batch := a.executeBatch(context.Background(), &a.turn, calls)
	if batch.err != nil || len(batch.results) != 1 || !strings.Contains(batch.results[0], "tool_retired") {
		t.Fatalf("retired result = %+v", batch)
	}
	if got := toolResultByID(a.Session(), "old"); !strings.Contains(got, "tool_retired") {
		t.Fatalf("stored paired result = %q", got)
	}
}
