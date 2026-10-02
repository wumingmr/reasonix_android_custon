package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"reasonix/internal/tool"
)

// Keep a hidden tombstone so stale model requests receive an actionable error
// without restoring the retired completion state machine.
func init() { tool.RegisterBuiltin(completeStep{}) }

type completeStep struct{}

func (completeStep) Name() string { return "complete_step" }
func (completeStep) Description() string {
	return "Retired. Replace the complete flat task list with todo_write."
}
func (completeStep) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":true}`)
}
func (completeStep) ReadOnly() bool                       { return true }
func (completeStep) ProviderVisible(context.Context) bool { return false }
func (completeStep) PlanModeSafe() bool                   { return true }
func (completeStep) Execute(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("complete_step is retired; replace the complete flat list with todo_write")
}
