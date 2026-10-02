package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

type recordingCheckpointer struct {
	mu         sync.Mutex
	boundaries []SessionCheckpointBoundary
	fail       SessionCheckpointBoundary
}

func (c *recordingCheckpointer) CheckpointSession(_ context.Context, boundary SessionCheckpointBoundary) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.boundaries = append(c.boundaries, boundary)
	if boundary == c.fail {
		return errors.New("checkpoint unavailable")
	}
	return nil
}

type checkpointTool struct{ calls int }

func (t *checkpointTool) Name() string            { return "checkpoint_tool" }
func (t *checkpointTool) Description() string     { return "test" }
func (t *checkpointTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *checkpointTool) ReadOnly() bool          { return true }
func (t *checkpointTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.calls++
	return "ok", nil
}

func TestModelCheckpointFailurePreventsProviderDispatch(t *testing.T) {
	providerStub := &scriptedProvider{name: "model", turns: [][]provider.Chunk{{{Type: provider.ChunkDone}}}}
	checkpointer := &recordingCheckpointer{fail: CheckpointBeforeModel}
	agent := New(providerStub, tool.NewRegistry(), NewSession("sys"), Options{SessionCheckpointer: checkpointer}, event.Discard)

	if err := agent.Run(t.Context(), "hello"); err == nil || providerStub.call != 0 {
		t.Fatalf("Run error/provider calls = %v/%d", err, providerStub.call)
	}
}

func TestToolCheckpointFailurePreventsToolBody(t *testing.T) {
	target := &checkpointTool{}
	registry := tool.NewRegistry()
	registry.Add(target)
	providerStub := &scriptedProvider{name: "model", turns: [][]provider.Chunk{{
		toolCallChunk("call-1", target.Name(), `{}`),
		{Type: provider.ChunkDone},
	}, {
		{Type: provider.ChunkText, Text: "tool did not run"},
		{Type: provider.ChunkDone},
	}}}
	checkpointer := &recordingCheckpointer{fail: CheckpointBeforeTopTool}
	agent := New(providerStub, registry, NewSession("sys"), Options{SessionCheckpointer: checkpointer}, event.Discard)

	if err := agent.Run(t.Context(), "run it"); err != nil {
		t.Fatal(err)
	}
	if target.calls != 0 {
		t.Fatalf("tool body ran %d times", target.calls)
	}
	if providerStub.call != 2 {
		t.Fatalf("provider calls = %d, want 2", providerStub.call)
	}
}

var _ tool.Tool = (*checkpointTool)(nil)
