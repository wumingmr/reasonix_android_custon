package builtin

import (
	"context"
	"testing"

	"reasonix/internal/jobs"
	"reasonix/internal/tool"
)

func TestContextualBuiltinVisibilityFollowsOwningContext(t *testing.T) {
	goal, _ := tool.LookupBuiltin("update_goal")
	jobNames := []string{"bash_output", "wait", "kill_shell"}

	if goal.(tool.ContextualTool).ProviderVisible(context.Background()) {
		t.Fatal("update_goal visible without a Goal lifecycle owner")
	}
	if !goal.(tool.ContextualTool).ProviderVisible(goalLifecycleContext(&goalLifecycleStub{view: goalView()}, tool.GoalSourceDirectHuman)) {
		t.Fatal("update_goal hidden with a Goal lifecycle owner")
	}
	if _, ok := tool.LookupBuiltin("complete_step"); ok {
		t.Fatal("retired complete_step remains discoverable")
	}
	for _, name := range jobNames {
		t.Run(name, func(t *testing.T) {
			candidate, ok := tool.LookupBuiltin(name)
			if !ok {
				t.Fatal("missing builtin")
			}
			contextual := candidate.(tool.ContextualTool)
			if contextual.ProviderVisible(context.Background()) {
				t.Fatal("job tool visible without a manager")
			}
			if !contextual.ProviderVisible(jobs.WithManager(context.Background(), &jobs.Manager{})) {
				t.Fatal("job tool hidden with an owned manager")
			}
		})
	}
}
