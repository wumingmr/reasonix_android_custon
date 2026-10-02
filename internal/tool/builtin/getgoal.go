package builtin

import (
	"context"
	"encoding/json"

	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(getGoal{}) }

type getGoal struct{}

func (getGoal) Name() string { return "get_goal" }
func (getGoal) Description() string {
	return "Read the current same-session goal, including its exact id/revision, objective, phase, admitted rounds, optional round limit, blocker reason, and live activation. Call this before update_goal."
}
func (getGoal) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`)
}
func (getGoal) ReadOnly() bool     { return true }
func (getGoal) PlanModeSafe() bool { return true }
func (getGoal) ProviderVisible(ctx context.Context) bool {
	_, ok := tool.GoalLifecycleFromContext(ctx)
	return ok
}
func (getGoal) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct{}
	if err := decodeGoalArgs(args, &input, "get_goal"); err != nil {
		return "", err
	}
	binding, err := goalBinding(ctx)
	if err != nil {
		return "", err
	}
	view, err := binding.Owner.GetGoal(ctx)
	if err != nil {
		return "", goalToolError("get_goal", err)
	}
	return goalToolResult(view)
}
