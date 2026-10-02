package agent

import (
	"context"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
	"testing"
)

func standardTodoTestAgent(t *testing.T, turns [][]provider.Chunk) (*Agent, *scriptedProvider) {
	t.Helper()
	todoWrite, ok := tool.LookupBuiltin("todo_write")
	if !ok {
		t.Fatal("todo_write builtin not registered")
	}
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "write_file", readOnly: false})
	reg.Add(todoWrite)
	prov := &scriptedProvider{name: "p", turns: turns}
	return New(prov, reg, NewSession("stable-system-prefix"), Options{}, event.Discard), prov
}

func TestOrdinaryTurnDoesNotContinueForPendingTodos(t *testing.T) {
	a, prov := standardTodoTestAgent(t, [][]provider.Chunk{
		{toolCallChunk("todo", "todo_write", `{"todos":[{"content":"Edit code","status":"in_progress"}]}`), {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "I will edit it next."}, {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "must not be consumed"}, {Type: provider.ChunkDone}},
	})
	if err := a.Run(context.Background(), "make the change"); err != nil {
		t.Fatal(err)
	}
	if prov.call != 2 {
		t.Fatalf("provider calls=%d, want todo plus final", prov.call)
	}
	todos := a.CanonicalTodoState()
	if len(todos) != 1 || todos[0].Status != "in_progress" {
		t.Fatalf("todo facts changed: %+v", todos)
	}
}
