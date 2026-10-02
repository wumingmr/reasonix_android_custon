package control

import (
	"context"
	"encoding/json"
	"fmt"

	"reasonix/internal/event"
	goaldomain "reasonix/internal/goal"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

const defaultBlockedAfterRounds uint64 = 3

func (c *Controller) GetGoal(context.Context) (*goaldomain.View, error) {
	return c.goalLifecycleView()
}

func (c *Controller) CreateGoal(ctx context.Context, request goaldomain.CreateRequest, authority tool.GoalAuthority) (goaldomain.View, error) {
	view, err := c.applyGoalMutation(ctx, authority, tool.GoalActionCreate, func(machine *goaldomain.Machine) (goaldomain.View, error) {
		return machine.Create(request)
	})
	if err == nil {
		c.resetGoalResourceBudget()
	}
	return view, err
}

func (c *Controller) UpdateGoal(ctx context.Context, request tool.GoalUpdateRequest, authority tool.GoalAuthority) (goaldomain.View, error) {
	return c.applyGoalMutation(ctx, authority, request.Action, func(machine *goaldomain.Machine) (goaldomain.View, error) {
		switch request.Action {
		case tool.GoalActionEdit:
			if request.BlockedReason != nil {
				return goaldomain.View{}, &goaldomain.Error{Code: goaldomain.ErrInvalidEdit, Message: "blocked_reason is valid only with action blocked"}
			}
			return machine.Edit(request.Ref, goaldomain.EditRequest{Objective: request.Objective, MaxGoalRounds: request.MaxGoalRounds})
		case tool.GoalActionPause:
			return machine.Pause(request.Ref)
		case tool.GoalActionResume:
			if current := machine.Get(); current != nil && current.Phase == goaldomain.PhasePaused {
				return goaldomain.View{}, &goaldomain.Error{Code: goaldomain.ErrUserAuthorityRequired, Message: "a user-paused goal must be resumed through an explicit UI or command action"}
			}
			return machine.Resume(request.Ref, authority.Source == tool.GoalSourceDirectHuman)
		case tool.GoalActionComplete:
			return machine.Complete(request.Ref)
		case tool.GoalActionBlocked:
			if request.BlockedReason == nil {
				return goaldomain.View{}, &goaldomain.Error{Code: goaldomain.ErrInvalidBlockReason, Message: "blocked action requires blocked_reason"}
			}
			return machine.Block(request.Ref, *request.BlockedReason, authority.Source == tool.GoalSourceDirectHuman, defaultBlockedAfterRounds)
		default:
			return goaldomain.View{}, &goaldomain.Error{Code: goaldomain.ErrInvalidTransition, Message: fmt.Sprintf("unsupported goal action %q", request.Action)}
		}
	})
}

func (c *Controller) applyGoalMutation(
	ctx context.Context,
	authority tool.GoalAuthority,
	action tool.GoalAction,
	mutate func(*goaldomain.Machine) (goaldomain.View, error),
) (goaldomain.View, error) {
	if c == nil || mutate == nil {
		return goaldomain.View{}, session.ErrSessionNotRunning
	}
	c.goalLifecycleMutationMu.Lock()
	defer c.goalLifecycleMutationMu.Unlock()
	runtime, err := c.validateGoalAuthority(authority, action)
	if err != nil {
		return goaldomain.View{}, err
	}
	c.goalLifecycleMu.RLock()
	machine, loadErr := c.goalLifecycle, c.goalLifecycleLoadErr
	c.goalLifecycleMu.RUnlock()
	if loadErr != nil {
		return goaldomain.View{}, fmt.Errorf("goal lifecycle is unavailable: %w", loadErr)
	}
	if machine == nil {
		return goaldomain.View{}, session.ErrSessionNotRunning
	}
	candidate := machine.Clone()
	view, err := mutate(candidate)
	if err != nil {
		return goaldomain.View{}, err
	}
	payload, err := candidate.Encode()
	if err != nil {
		return goaldomain.View{}, err
	}
	operationID := fmt.Sprintf("goal:%s:%s:%d:%s", runtime.Ref().SessionID, view.ID, view.Revision, action)
	appendCtx := ctx
	if appendCtx == nil || appendCtx.Err() != nil {
		appendCtx = context.Background()
	}
	if _, err := runtime.Session().Append(appendCtx, session.Batch{
		OperationID: operationID,
		TurnID:      runtime.Session().ExecutionSnapshot().Projection.TurnID,
		Events:      []session.Event{{Kind: "goal/state", Payload: json.RawMessage(payload)}},
	}); err != nil {
		return goaldomain.View{}, err
	}
	// Session.Append is the final authority check. Once accepted, goal/state is
	// a session fact and must be published locally; a stale error here could make
	// the model repeat a mutation whose durability is already certain.
	_, currentRuntime, stillExclusive := c.v3Binding()
	if !stillExclusive || currentRuntime != runtime {
		// The accepted fact belongs to the runtime that authorized this tool
		// call. A concurrent session switch installs its own projection, so do
		// not publish the old session's candidate into the new runtime.
		return view, nil
	}
	c.goalLifecycleMu.Lock()
	if c.goalLifecycle == machine && c.goalLifecycleLoadErr == nil {
		c.goalLifecycle = candidate
	}
	c.goalLifecycleMu.Unlock()
	c.refreshRuntimeState(event.Event{})
	return view, nil
}

func (c *Controller) validateGoalAuthority(authority tool.GoalAuthority, action tool.GoalAction) (*session.Runtime, error) {
	if !authority.Allows(action) {
		return nil, &goaldomain.Error{Code: goaldomain.ErrUserAuthorityRequired, Message: "current execution is not allowed to perform this goal action"}
	}
	_, runtime, exclusive := c.v3Binding()
	if !exclusive || runtime == nil {
		return nil, session.ErrSessionNotRunning
	}
	snapshot := runtime.StateSnapshot()
	if snapshot.Phase != session.RuntimeRunning ||
		authority.SessionID != snapshot.Ref.SessionID ||
		authority.RuntimeEpoch != snapshot.Epoch ||
		authority.ActivityID != snapshot.ActivityRevision {
		return nil, &goaldomain.Error{Code: goaldomain.ErrUserAuthorityRequired, Message: "goal authority no longer matches the active runtime"}
	}
	if authority.Source == tool.GoalSourceGoalRound {
		view, err := c.goalLifecycleView()
		if err != nil {
			return nil, err
		}
		if view == nil || view.ID != authority.GoalID || view.Revision != authority.Revision || view.RoundsStarted != authority.Round {
			return nil, &goaldomain.Error{Code: goaldomain.ErrStaleRevision, Message: "goal round authority is stale"}
		}
	}
	return runtime, nil
}
