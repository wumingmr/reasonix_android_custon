package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	goaldomain "reasonix/internal/goal"
	"reasonix/internal/tool"
)

type goalToolValue struct {
	Goal        *goaldomain.Snapshot  `json:"goal"`
	Activation  goaldomain.Activation `json:"activation,omitempty"`
	StopReason  string                `json:"stopReason,omitempty"`
	Instruction string                `json:"instruction,omitempty"`
}

type optionalRoundLimit struct {
	Present bool
	Raw     json.RawMessage
}

func (o *optionalRoundLimit) UnmarshalJSON(data []byte) error {
	o.Present = true
	o.Raw = append(o.Raw[:0], data...)
	return nil
}

func goalToolResult(view *goaldomain.View) (string, error) {
	return goalToolResultWithInstruction(view, "")
}

func goalToolResultWithInstruction(view *goaldomain.View, instruction string) (string, error) {
	value := goalToolValue{}
	if view != nil {
		snapshot := view.Snapshot
		value.Goal = &snapshot
		value.Activation = view.Activation
		value.StopReason = view.StopReason
	}
	value.Instruction = instruction
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode goal result: %w", err)
	}
	return string(encoded), nil
}

func goalBinding(ctx context.Context) (tool.GoalLifecycleBinding, error) {
	binding, ok := tool.GoalLifecycleFromContext(ctx)
	if !ok {
		return tool.GoalLifecycleBinding{}, fmt.Errorf("goal tool requires a current host-attested goal context")
	}
	return binding, nil
}

func goalToolError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if code := goaldomain.ErrorCodeOf(err); code != "" {
		return fmt.Errorf("%s [%s]: %w", operation, code, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func decodeGoalArgs(args json.RawMessage, target any, toolName string) error {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid %s arguments: %w", toolName, err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return fmt.Errorf("invalid %s arguments: %w", toolName, err)
	}
	return nil
}

func parseRoundLimit(raw json.RawMessage) (*uint64, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value uint64
	if err := json.Unmarshal(raw, &value); err != nil || value == 0 {
		return nil, fmt.Errorf("max_goal_rounds must be null or a positive integer")
	}
	return &value, nil
}

func trimmedRequired(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required and must be non-empty", field)
	}
	return value, nil
}
