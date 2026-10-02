package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(todoWrite{}) }

// todoWrite replaces the current turn's complete, flat task list. The list is
// model-managed progress UI; it is deliberately independent from Plan and Goal
// authorization and from delivery evidence.
type todoWrite struct{}

type todoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

type todoWriteResponse struct {
	Todos  []todoItem `json:"todos"`
	Counts struct {
		Total      int `json:"total"`
		Pending    int `json:"pending"`
		InProgress int `json:"in_progress"`
		Completed  int `json:"completed"`
	} `json:"counts"`
}

func (todoWrite) Name() string { return "todo_write" }

func (todoWrite) Description() string {
	return "Replace the current turn's complete task list. Send the full flat list on every call; an empty list clears it. Items may be reordered, removed, replanned, or have any number in progress. Each item contains only content and status (pending|in_progress|completed)."
}

func (todoWrite) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"additionalProperties":false,
"properties":{
  "todos":{
    "type":"array",
    "description":"The complete flat task list for this turn. Replaces the previous list; [] clears it.",
    "items":{
      "type":"object",
      "additionalProperties":false,
      "properties":{
        "content":{"type":"string","minLength":1,"description":"Task text. Leading and trailing whitespace is removed."},
        "status":{"type":"string","enum":["pending","in_progress","completed"]}
      },
      "required":["content","status"]
    }
  }
},
"required":["todos"]
}`)
}

func (todoWrite) ReadOnly() bool { return true }

func (todoWrite) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Todos *[]json.RawMessage `json:"todos"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return "", fmt.Errorf("invalid todo_write arguments: %w; send only {todos:[{content,status}]}", err)
	}
	if err := ensureJSONEnd(dec); err != nil {
		return "", fmt.Errorf("invalid todo_write arguments: %w", err)
	}
	if p.Todos == nil {
		return "", fmt.Errorf("todos is required and must be an array")
	}

	rawTodos := *p.Todos
	response := todoWriteResponse{Todos: make([]todoItem, len(rawTodos))}
	seen := make(map[string]int, len(rawTodos))
	for i, raw := range rawTodos {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return "", fmt.Errorf("todos[%d] must be an object with content and status", i)
		}
		var item todoItem
		itemDecoder := json.NewDecoder(bytes.NewReader(raw))
		itemDecoder.DisallowUnknownFields()
		if err := itemDecoder.Decode(&item); err != nil {
			return "", fmt.Errorf("todos[%d] is invalid: %w; use only content and status", i, err)
		}
		if err := ensureJSONEnd(itemDecoder); err != nil {
			return "", fmt.Errorf("todos[%d] is invalid: %w", i, err)
		}
		item.Content = strings.TrimSpace(item.Content)
		if item.Content == "" {
			return "", fmt.Errorf("todos[%d].content must be non-empty after trimming", i)
		}
		if previous, ok := seen[item.Content]; ok {
			return "", fmt.Errorf("todos[%d].content duplicates todos[%d].content %q; merge or rename one item", i, previous, item.Content)
		}
		seen[item.Content] = i
		switch item.Status {
		case "pending":
			response.Counts.Pending++
		case "in_progress":
			response.Counts.InProgress++
		case "completed":
			response.Counts.Completed++
		default:
			return "", fmt.Errorf("todos[%d].status %q is invalid; use pending, in_progress, or completed", i, item.Status)
		}
		response.Todos[i] = item
	}
	response.Counts.Total = len(response.Todos)
	out, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("encode todo_write result: %w", err)
	}
	return string(out), nil
}

func ensureJSONEnd(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("multiple JSON values are not allowed")
}
