package boot

import (
	"context"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

// A review skill runs on the step budget its runner resolved from the parent;
// the review contract bounds each response, not how many files it may read.
func TestReviewSkillKeepsRunnerStepBudget(t *testing.T) {
	for _, steps := range []int{0, 20} {
		var gotSteps int
		factory := func(_ context.Context, s int, _ *provider.Pricing, _ int, _ int) agent.Options {
			gotSteps = s
			return agent.Options{MaxSteps: s}
		}
		_, opts := reviewSubagentSkillOptions(context.Background(), "review", "review the change", steps, nil, 0, 1, factory)
		if gotSteps != steps {
			t.Fatalf("runner steps %d reached the child as %d", steps, gotSteps)
		}
		if opts.MaxOutputTokens != 2048 {
			t.Fatalf("review output cap = %d, want 2048", opts.MaxOutputTokens)
		}
	}
}
