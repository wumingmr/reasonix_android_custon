package tool

import (
	"context"

	"reasonix/internal/goal"
)

type GoalSource string

const (
	GoalSourceDirectHuman GoalSource = "direct-human"
	GoalSourceGoalRound   GoalSource = "goal-round"
)

type GoalAction string

const (
	GoalActionCreate   GoalAction = "create"
	GoalActionEdit     GoalAction = "edit"
	GoalActionPause    GoalAction = "pause"
	GoalActionResume   GoalAction = "resume"
	GoalActionComplete GoalAction = "complete"
	GoalActionBlocked  GoalAction = "blocked"
	GoalActionClear    GoalAction = "clear"
)

// GoalAuthority contains only host-attested execution identity. Model text and
// persisted messages cannot construct one; the top-level runtime supplies it
// after admitting a direct human turn or an exact autonomous goal round.
type GoalAuthority struct {
	Source       GoalSource
	SessionID    string
	RuntimeEpoch string
	ActivityID   uint64
	GoalID       string
	Revision     uint64
	Round        uint64
}

func (a GoalAuthority) Allows(action GoalAction) bool {
	switch a.Source {
	case GoalSourceDirectHuman:
		return action == GoalActionCreate || action == GoalActionEdit ||
			action == GoalActionPause || action == GoalActionResume ||
			action == GoalActionComplete || action == GoalActionBlocked ||
			action == GoalActionClear
	case GoalSourceGoalRound:
		return action == GoalActionComplete || action == GoalActionBlocked
	default:
		return false
	}
}

type GoalUpdateRequest struct {
	Ref           goal.Ref
	Action        GoalAction
	Objective     *string
	MaxGoalRounds goal.RoundLimitChange
	BlockedReason *goal.BlockReason
}

// GoalLifecycleOwner is the single host-owned bridge between model-facing
// tools and the durable goal service. Implementations must revalidate Authority
// against the current session runtime immediately before accepting a mutation.
type GoalLifecycleOwner interface {
	GetGoal(context.Context) (*goal.View, error)
	CreateGoal(context.Context, goal.CreateRequest, GoalAuthority) (goal.View, error)
	UpdateGoal(context.Context, GoalUpdateRequest, GoalAuthority) (goal.View, error)
}

type GoalLifecycleBinding struct {
	Owner     GoalLifecycleOwner
	Authority GoalAuthority
}

type goalLifecycleKey struct{}
type noGoalLifecycle struct{}

func WithGoalLifecycle(ctx context.Context, owner GoalLifecycleOwner, authority GoalAuthority) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if owner == nil {
		return ctx
	}
	return context.WithValue(ctx, goalLifecycleKey{}, GoalLifecycleBinding{Owner: owner, Authority: authority})
}

func WithoutGoalLifecycle(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, goalLifecycleKey{}, noGoalLifecycle{})
}

func GoalLifecycleFromContext(ctx context.Context) (GoalLifecycleBinding, bool) {
	if ctx == nil {
		return GoalLifecycleBinding{}, false
	}
	binding, ok := ctx.Value(goalLifecycleKey{}).(GoalLifecycleBinding)
	return binding, ok && binding.Owner != nil
}
