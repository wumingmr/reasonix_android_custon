package builtin

import (
	"context"
	"encoding/json"

	goaldomain "reasonix/internal/goal"
	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(createGoal{}) }

type createGoal struct{}

func (createGoal) Name() string { return "create_goal" }
func (createGoal) Description() string {
	return "Create one persisted same-session goal when the current direct human request is a long-running objective that should continue across autonomous rounds. You may infer that intent without requiring the user to name goal mode. Do not use this for routine single-turn work."
}
func (createGoal) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"objective":{"type":"string","minLength":1},"max_goal_rounds":{"anyOf":[{"type":"integer","minimum":1},{"type":"null"}],"description":"Optional positive automatic-round limit; omit or null for unlimited."}},"required":["objective"]}`)
}
func (createGoal) ReadOnly() bool { return false }
func (createGoal) ProviderVisible(ctx context.Context) bool {
	_, ok := tool.GoalLifecycleFromContext(ctx)
	return ok
}
func (createGoal) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Objective string             `json:"objective"`
		Limit     optionalRoundLimit `json:"max_goal_rounds"`
	}
	if err := decodeGoalArgs(args, &input, "create_goal"); err != nil {
		return "", err
	}
	objective, err := trimmedRequired(input.Objective, "objective")
	if err != nil {
		return "", err
	}
	var limit *uint64
	if input.Limit.Present {
		limit, err = parseRoundLimit(input.Limit.Raw)
		if err != nil {
			return "", err
		}
	}
	binding, err := goalBinding(ctx)
	if err != nil {
		return "", err
	}
	view, err := binding.Owner.CreateGoal(ctx, goaldomain.CreateRequest{Objective: objective, MaxGoalRounds: limit}, binding.Authority)
	if err != nil {
		return "", goalToolError("create_goal", err)
	}
	return goalToolResult(&view)
}
