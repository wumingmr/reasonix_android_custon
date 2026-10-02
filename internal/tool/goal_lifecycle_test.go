package tool

import (
	"context"
	"testing"

	"reasonix/internal/goal"
)

type lifecycleOwnerStub struct{}

func (lifecycleOwnerStub) GetGoal(context.Context) (*goal.View, error) { return nil, nil }
func (lifecycleOwnerStub) CreateGoal(context.Context, goal.CreateRequest, GoalAuthority) (goal.View, error) {
	return goal.View{}, nil
}
func (lifecycleOwnerStub) UpdateGoal(context.Context, GoalUpdateRequest, GoalAuthority) (goal.View, error) {
	return goal.View{}, nil
}

func TestGoalLifecycleBindingCarriesHostAttestedAuthority(t *testing.T) {
	authority := GoalAuthority{
		Source:       GoalSourceDirectHuman,
		SessionID:    "session-1",
		RuntimeEpoch: "epoch-1",
		ActivityID:   7,
	}
	ctx := WithGoalLifecycle(context.Background(), lifecycleOwnerStub{}, authority)
	binding, ok := GoalLifecycleFromContext(ctx)
	if !ok {
		t.Fatal("goal lifecycle binding is missing")
	}
	if binding.Authority != authority {
		t.Fatalf("authority = %+v", binding.Authority)
	}
}

func TestWithoutGoalLifecycleShadowsParentAuthority(t *testing.T) {
	parent := WithGoalLifecycle(context.Background(), lifecycleOwnerStub{}, GoalAuthority{Source: GoalSourceGoalRound})
	child := WithoutGoalLifecycle(parent)
	if _, ok := GoalLifecycleFromContext(child); ok {
		t.Fatal("child inherited parent goal authority")
	}
}

func TestGoalAuthorityClassifiesMutationRights(t *testing.T) {
	human := GoalAuthority{Source: GoalSourceDirectHuman}
	round := GoalAuthority{Source: GoalSourceGoalRound, GoalID: "goal-1", Revision: 3, Round: 2}
	for _, action := range []GoalAction{GoalActionCreate, GoalActionEdit, GoalActionPause, GoalActionResume, GoalActionClear} {
		if !human.Allows(action) {
			t.Fatalf("direct human authority rejected %q", action)
		}
		if round.Allows(action) {
			t.Fatalf("goal round authority allowed %q", action)
		}
	}
	for _, action := range []GoalAction{GoalActionComplete, GoalActionBlocked} {
		if !human.Allows(action) || !round.Allows(action) {
			t.Fatalf("terminal action %q was rejected", action)
		}
	}
	if (GoalAuthority{}).Allows(GoalActionComplete) {
		t.Fatal("empty authority may not mutate a goal")
	}
}
