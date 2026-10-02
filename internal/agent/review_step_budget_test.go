package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// A review child under an uncapped parent takes as many rounds as the review
// needs; a multi-file review that reads more than a handful of files must not
// stop at a budget the user has no setting for.
func TestReviewSubagentUnderUncappedParentFinishesPastEightRounds(t *testing.T) {
	const rounds = 12
	turns := make([]testutil.Turn, 0, rounds+1)
	for i := range rounds {
		turns = append(turns, testutil.Turn{ToolCalls: []provider.ToolCall{{
			ID: fmt.Sprintf("read-%d", i), Name: "echo", Arguments: fmt.Sprintf(`{"text":"file %d"}`, i),
		}}})
	}
	turns = append(turns, testutil.Turn{Text: "verdict: approve"})
	parent := tool.NewRegistry()
	parent.Add(echoTool{})
	task := NewTaskTool(testutil.NewMock("sub", turns...), nil, parent, 0, 0, 0, 0, 0, 0, 0, 0.0, "", "sys", nil, 0, "", "", nil).
		WithTranscripts(mustSubagentStore(t), t.TempDir(), "base", "high").
		WithProfileLookup(func(name string) (ProfileDefinition, bool) {
			return ProfileDefinition{Name: name, Body: "You review changes."}, name == "review"
		})

	out, err := task.Execute(withCallContext(context.Background(), "c", event.Discard, nil, false),
		json.RawMessage(`{"prompt":"review the change","profile":"review"}`))
	if err != nil {
		t.Fatalf("review task: %v", err)
	}
	if strings.Contains(out, "paused after") || !strings.Contains(out, "verdict: approve") {
		t.Fatalf("review child must finish its review, got:\n%s", out)
	}
}

func TestReviewBudgetKeepsInheritedStepsAndBoundsOutput(t *testing.T) {
	for _, tc := range []struct {
		parent, requested, want int
	}{
		{parent: 0, requested: 0, want: 0},
		{parent: 40, requested: 0, want: 20},
		{parent: 0, requested: 24, want: 24},
	} {
		task := &TaskTool{maxSteps: tc.parent}
		spec := ProfileExecSpec{Worker: WorkerSpec{Profile: "review"}, Sched: SchedulerPolicy{MaxSteps: tc.requested}}
		ctx, steps := task.childMaxStepsForSpec(context.Background(), &spec)
		if steps != tc.want {
			t.Fatalf("parent=%d requested=%d: steps = %d, want %d", tc.parent, tc.requested, steps, tc.want)
		}
		if childOutputBudgetFrom(ctx) != defaultReviewOutputTokens {
			t.Fatalf("review output budget = %d", childOutputBudgetFrom(ctx))
		}
	}
}
