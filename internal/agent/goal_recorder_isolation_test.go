package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"reasonix/internal/event"
	goaldomain "reasonix/internal/goal"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

type childIsolationGoalOwner struct{ updates int }

func (*childIsolationGoalOwner) GetGoal(context.Context) (*goaldomain.View, error) { return nil, nil }
func (*childIsolationGoalOwner) CreateGoal(context.Context, goaldomain.CreateRequest, tool.GoalAuthority) (goaldomain.View, error) {
	return goaldomain.View{}, nil
}
func (o *childIsolationGoalOwner) UpdateGoal(context.Context, tool.GoalUpdateRequest, tool.GoalAuthority) (goaldomain.View, error) {
	o.updates++
	return goaldomain.View{}, nil
}

func TestSubAgentDoesNotInheritParentGoalRecorder(t *testing.T) {
	goalTool, ok := tool.LookupBuiltin("update_goal")
	if !ok {
		t.Fatal("update_goal builtin not registered")
	}
	reg := tool.NewRegistry()
	reg.Add(goalTool)
	prov := &scriptedProvider{name: "goal-child", turns: [][]provider.Chunk{
		{toolCallChunk("goal", "update_goal", `{"goal_id":"goal-1","revision":1,"action":"complete"}`), {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "Child result."}, {Type: provider.ChunkDone}},
	}}
	owner := &childIsolationGoalOwner{}
	ctx := tool.WithGoalLifecycle(context.Background(), owner, tool.GoalAuthority{Source: tool.GoalSourceGoalRound})
	sess := NewSession("child system")
	answer, err := RunSubAgentWithSession(ctx, prov, reg, sess, "inspect the task", Options{}, event.Discard)
	if err != nil {
		t.Fatalf("Goal child: %v", err)
	}
	if answer != "Child result." {
		t.Fatalf("Goal child answer = %q", answer)
	}
	for i, req := range prov.requests {
		if !slices.Contains(toolSchemaNames(req.Tools), "update_goal") {
			t.Fatalf("child request %d changed the static tool surface: %v", i+1, toolSchemaNames(req.Tools))
		}
	}
	if owner.updates != 0 {
		t.Fatalf("child mutated parent Goal: %d updates", owner.updates)
	}
	if got := lastToolResult(sess, "update_goal"); !strings.Contains(got, "top-level host-attested goal context") {
		t.Fatalf("child update_goal result = %q", got)
	}
}

func TestCoordinatorPlannerCannotReportExecutorGoalDisposition(t *testing.T) {
	goalTool, ok := tool.LookupBuiltin("update_goal")
	if !ok {
		t.Fatal("update_goal builtin not registered")
	}
	reg := tool.NewRegistry()
	reg.Add(goalTool)
	planner := &mockProvider{name: "planner", streams: [][]provider.Chunk{
		{toolCallChunk("planner-goal", "update_goal", `{"goal_id":"goal-1","revision":1,"action":"complete"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("plan-1", "submit_plan", `{"objective":"inspect the implementation","steps":[{"title":"apply and verify the fix"}]}`), {Type: provider.ChunkDone}},
	}}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{{Type: provider.ChunkText, Text: "Implemented and verified."}, {Type: provider.ChunkDone}}}
	plannerSess := NewSession("planner-sys")
	executor := New(exec, reg, NewSession("exec-sys"), Options{}, event.Discard)
	customPlannerReg := tool.NewRegistry()
	customPlannerReg.Add(goalTool)
	coord := NewCoordinator(planner, plannerSess, nil, PlannerToolRegistry(customPlannerReg), Options{}, executor, 0, event.Discard, nil)
	owner := &childIsolationGoalOwner{}
	ctx := withNoClosedLoop(tool.WithGoalLifecycle(context.Background(), owner, tool.GoalAuthority{Source: tool.GoalSourceDirectHuman}))
	if err := coord.Run(ctx, "fix the goal bug"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i, req := range planner.requests {
		if got := toolSchemaNames(req.Tools); slices.Contains(got, "update_goal") || !slices.Equal(got, []string{"submit_plan"}) {
			t.Fatalf("planner request %d exposed goal mutation outside the top-level host: %v", i+1, got)
		}
	}
	if got := lastToolResult(plannerSess, "update_goal"); !strings.Contains(got, "unknown tool") {
		t.Fatalf("planner goal mutation did not fail closed: %q", got)
	}
	for i, req := range exec.requests {
		if !slices.Contains(toolSchemaNames(req.Tools), "update_goal") {
			t.Fatalf("executor request %d lost update_goal: %v", i+1, toolSchemaNames(req.Tools))
		}
	}
	if owner.updates != 0 {
		t.Fatalf("planner mutated executor Goal: %d updates", owner.updates)
	}
}
