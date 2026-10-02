package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	goaldomain "reasonix/internal/goal"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

type restoredGoalRunner struct {
	mu      sync.Mutex
	sources []tool.GoalSource
	done    chan struct{}
}

func (r *restoredGoalRunner) Run(ctx context.Context, _ string) error {
	binding, ok := tool.GoalLifecycleFromContext(ctx)
	if !ok {
		return context.Canceled
	}
	r.mu.Lock()
	r.sources = append(r.sources, binding.Authority.Source)
	call := len(r.sources)
	r.mu.Unlock()
	view, err := binding.Owner.GetGoal(ctx)
	if err != nil || view == nil {
		return err
	}
	switch call {
	case 1:
		if view.Phase != goaldomain.PhaseActive || view.Activation != goaldomain.ActivationDisarmed {
			return errors.New("restored goal was not active/disarmed")
		}
		// Exercise the model-facing schema and decoder with the redundant fields
		// models commonly echo after get_goal during cold-session recovery.
		resumeTool, ok := tool.LookupBuiltin("update_goal")
		if !ok {
			return errors.New("update_goal is not registered")
		}
		args, err := json.Marshal(map[string]any{
			"goal_id": view.ID, "revision": view.Revision, "action": "resume",
			"objective": "echoed objective must not replace the target", "max_goal_rounds": nil, "blocked_reason": "",
		})
		if err != nil {
			return err
		}
		validation := tool.ValidateArguments(resumeTool, args)
		if validation.CompileErr != nil || len(validation.Violations) != 0 {
			return errors.New("resume arguments failed host validation")
		}
		result, err := resumeTool.Execute(ctx, args)
		if err == nil && !strings.Contains(result, "Fields not applied: objective, max_goal_rounds, blocked_reason.") {
			return errors.New("resume result did not disclose ignored fields")
		}
		return err
	case 2:
		return nil
	case 3:
		_, err = binding.Owner.UpdateGoal(ctx, tool.GoalUpdateRequest{Ref: view.Ref(), Action: tool.GoalActionComplete}, binding.Authority)
		close(r.done)
		return err
	default:
		return errors.New("unexpected extra goal round")
	}
}

func TestColdRestoredGoalCanResumeFromNaturalUserRequestAndContinue(t *testing.T) {
	root := t.TempDir()
	persistence := session.NewFilesystemPersistence(root)
	seedService, err := session.NewService("desktop", persistence)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seedService.CloseAll(context.Background()) })
	seedRuntime, err := seedService.Create(t.Context(), session.CreateOptions{SessionID: "restored-goal"})
	if err != nil {
		t.Fatal(err)
	}
	machine := goaldomain.NewMachine(nil, func() string { return "restored-goal-id" })
	limit := uint64(10)
	if _, err := machine.Create(goaldomain.CreateRequest{Objective: "finish the restored target", MaxGoalRounds: &limit}); err != nil {
		t.Fatal(err)
	}
	payload, err := machine.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedRuntime.Session().Append(t.Context(), session.Batch{OperationID: "seed-goal", Events: []session.Event{{Kind: "goal/state", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	if err := seedService.Close(t.Context(), seedRuntime.Ref()); err != nil {
		t.Fatal(err)
	}

	service, err := session.NewService("desktop", persistence)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	binding, err := service.Open(t.Context(), session.SessionRef{HostID: "desktop", SessionID: "restored-goal"})
	if err != nil {
		t.Fatal(err)
	}
	runtime := binding.Runtime()
	runner := &restoredGoalRunner{done: make(chan struct{})}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Runner: runner, Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	cleanupGoalDriverController(t, c)
	t.Cleanup(func() { _ = binding.Release(context.Background()) })
	c.Send("继续把这个目标做完")
	select {
	case <-runner.done:
	case <-time.After(5 * time.Second):
		t.Fatal("restored goal did not resume and continue")
	}
	view, err := c.goalLifecycleView()
	if err != nil || view == nil || view.Phase != goaldomain.PhaseComplete || view.RoundsStarted != 2 {
		t.Fatalf("restored goal view = %+v, err = %v", view, err)
	}
	if view.Objective != "finish the restored target" || view.MaxGoalRounds == nil || *view.MaxGoalRounds != limit {
		t.Fatalf("resume changed the restored goal definition: %+v", view)
	}
	runner.mu.Lock()
	sources := append([]tool.GoalSource(nil), runner.sources...)
	runner.mu.Unlock()
	if len(sources) != 3 || sources[0] != tool.GoalSourceDirectHuman || sources[1] != tool.GoalSourceGoalRound || sources[2] != tool.GoalSourceGoalRound {
		t.Fatalf("restored goal authorities = %v", sources)
	}
}
