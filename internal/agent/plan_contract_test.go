package agent

import (
	"context"
	"reasonix/internal/event"
	"reasonix/internal/plancontract"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
	"reflect"
	"testing"
)

func contractPlan() plancontract.Plan {
	return plancontract.Plan{
		Objective: "make the cache key model-aware",
		Steps: []plancontract.Step{
			{
				ID: "p1", Title: "thread the model ref through",
				VerifiedFiles:  []string{"internal/provider/cache.go"},
				CandidateFiles: []string{"internal/boot/boot.go"},
				Risks:          []string{"warm caches invalidate once"},
				Acceptance: []plancontract.Criterion{
					{Text: "two model refs never share an entry"},
					{Text: "existing hits keep hitting", Regression: true},
					{Text: "the hit rate is logged", Optional: true},
				},
				Verification: []plancontract.Verification{{Command: "go test ./internal/provider/"}},
			},
		},
	}.Normalize()
}

func TestApprovedPlanRemainsInstructionData(t *testing.T) {
	plan := contractPlan()
	a := New(nil, tool.NewRegistry(), NewSession(""), Options{}, event.Discard)
	a.SetPlanContract(&plan)
	if !reflect.DeepEqual(a.planContractSnapshot(), &plan) {
		t.Fatal("plan content changed")
	}
	if !a.ReadinessResult().Ready {
		t.Fatal("plan content created a quality gate")
	}
}
func TestCoordinatorClearsThePlanContractEachTurn(t *testing.T) {
	planner := &mockProvider{name: "planner", streams: submitPlanCall(e2ePlanArgs)}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Done."},
		{Type: provider.ChunkDone},
	}}
	coord, executor := submitPlanCoordinator(t, planner, exec, event.Discard)

	if err := coord.Run(withNoClosedLoop(context.Background()), "fix the cache key"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if executor.planContractSnapshot() == nil {
		t.Fatal("the approved plan never reached the executor's contract")
	}

	coord.plannerPolicy = func(context.Context, string) PlannerDecision {
		return PlannerDecision{Route: PlannerRouteExecutorOnly, Reason: "test"}
	}
	if err := coord.Run(withNoClosedLoop(context.Background()), "just answer me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if executor.planContractSnapshot() != nil {
		t.Fatal("an executor-only turn inherited the previous turn's plan")
	}
}
