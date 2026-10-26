package builtin

import (
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/tool"
)

func TestGoalToolsExcludeObservationalWorkDuration(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool tool.Tool
		args string
	}{
		{"get", getGoal{}, `{}`},
		{"update", updateGoal{}, `{"goal_id":"goal-1","revision":3,"action":"pause"}`},
		{"create", createGoal{}, `{"objective":"ship"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &goalLifecycleStub{view: goalView()}
			ctx := goalLifecycleContext(stub, tool.GoalSourceDirectHuman)
			before, err := tc.tool.Execute(ctx, json.RawMessage(tc.args))
			if err != nil {
				t.Fatal(err)
			}
			stub.view.WorkDurationMs = 123456
			after, err := tc.tool.Execute(ctx, json.RawMessage(tc.args))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(after, "workDurationMs") || before != after {
				t.Fatalf("observational time changed tool payload:\nbefore: %s\nafter: %s", before, after)
			}
			if stub.view.WorkDurationMs != 123456 {
				t.Fatal("tool serialization mutated the host view")
			}
		})
	}
}
