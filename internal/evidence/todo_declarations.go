package evidence

import (
	"encoding/json"
	"strings"
)

// MatchStep resolves a step citation against the existing task list.
func MatchStep(step string, todos []TodoItem) (TodoStepMatch, bool) {
	m := matchTodoStep(step, todos)
	return m, m.Found
}

// ReplayTodoList leaves new model states intact and reads legacy lists with
// their original serial normalization. Neither path rewrites stored messages.
func ReplayTodoList(todos []TodoItem, output string) []TodoItem {
	var result struct {
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if json.Unmarshal([]byte(output), &result) == nil && result.Todos != nil {
		canonical := make([]TodoItem, len(result.Todos))
		for i, item := range result.Todos {
			canonical[i] = TodoItem{Content: item.Content, Status: item.Status}
		}
		return canonical
	}
	if strings.HasPrefix(output, "Model task list updated:") {
		return append([]TodoItem(nil), todos...)
	}
	return NormalizeSerialTodos(todos)
}

const ModelCompletionDeclarationPrefix = "Model completion declaration recorded for todo "

// ReplayTodoCompletion preserves legacy serial updates while replaying new
// declarations exactly as they happened, without advancing an unrelated item.
func ReplayTodoCompletion(todos []TodoItem, index int, output string) bool {
	if strings.HasPrefix(output, ModelCompletionDeclarationPrefix) {
		return CompleteDeclaredTodo(todos, index)
	}
	return AdvanceSerialTodo(todos, index)
}

// CompleteDeclaredTodo updates only the selected item, without advancing others.
func CompleteDeclaredTodo(todos []TodoItem, index int) bool {
	if index < 0 || index >= len(todos) || todos[index].Status == "completed" {
		return false
	}
	todos[index].Status = "completed"
	return true
}
