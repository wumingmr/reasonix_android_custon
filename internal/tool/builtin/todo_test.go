package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func executeTodos(t *testing.T, raw string) (todoWriteResponse, error) {
	t.Helper()
	out, err := (todoWrite{}).Execute(context.Background(), json.RawMessage(raw))
	if err != nil {
		return todoWriteResponse{}, err
	}
	var response todoWriteResponse
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("decode result %q: %v", out, err)
	}
	return response, nil
}

func TestTodoWriteAcceptsWholeListReplacementShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"empty", `{"todos":[]}`},
		{"only pending", `{"todos":[{"content":"one","status":"pending"},{"content":"two","status":"pending"}]}`},
		{"parallel", `{"todos":[{"content":"one","status":"in_progress"},{"content":"two","status":"in_progress"}]}`},
		{"out of order", `{"todos":[{"content":"later","status":"completed"},{"content":"earlier","status":"pending"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := executeTodos(t, tc.raw); err != nil {
				t.Fatalf("replacement rejected: %v", err)
			}
		})
	}
}

func TestTodoWriteReturnsCanonicalListAndCounts(t *testing.T) {
	got, err := executeTodos(t, `{"todos":[{"content":"  inspect  ","status":"completed"},{"content":"ship","status":"in_progress"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Todos) != 2 || got.Todos[0].Content != "inspect" || got.Counts.Total != 2 || got.Counts.Completed != 1 || got.Counts.InProgress != 1 {
		t.Fatalf("canonical response = %+v", got)
	}
}

func TestTodoWriteValidationIsStrictAndNonMutating(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"unknown item field", `{"todos":[{"content":"one","status":"pending","step_id":"old"}]}`, "unknown field"},
		{"unknown root field", `{"todos":[],"activeForm":"old"}`, "unknown field"},
		{"null item", `{"todos":[null]}`, "todos[0] must be an object"},
		{"missing content", `{"todos":[{"status":"pending"}]}`, "todos[0].content"},
		{"null content", `{"todos":[{"content":null,"status":"pending"}]}`, "todos[0].content"},
		{"missing status", `{"todos":[{"content":"one"}]}`, "todos[0].status"},
		{"null status", `{"todos":[{"content":"one","status":null}]}`, "todos[0].status"},
		{"blank", `{"todos":[{"content":"  ","status":"pending"}]}`, "todos[0].content"},
		{"duplicate after trim", `{"todos":[{"content":"one","status":"pending"},{"content":" one ","status":"completed"}]}`, "duplicates"},
		{"bad status", `{"todos":[{"content":"one","status":"running"}]}`, "todos[0].status"},
		{"trailing value", `{"todos":[]} {}`, "multiple JSON values"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := executeTodos(t, tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCompleteStepTombstoneIsActionable(t *testing.T) {
	_, err := (completeStep{}).Execute(context.Background(), json.RawMessage(`{"result":"done"}`))
	if err == nil || !strings.Contains(err.Error(), "retired") || !strings.Contains(err.Error(), "todo_write") {
		t.Fatalf("retirement error = %v", err)
	}
}
