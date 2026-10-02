package control

import (
	"context"
	"errors"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	goaldomain "reasonix/internal/goal"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func TestInheritLifecycleCarriesLiveGoalStateAcrossSameRuntimeRebuild(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "goal-rebuild"})
	if err != nil {
		t.Fatal(err)
	}
	newController := func() *Controller {
		exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
		return newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true, GoalTokenBudget: 1000})
	}
	old := newController()
	t.Cleanup(old.ReleaseResources)
	if err := old.SetGoalDurable("finish the rebuild"); err != nil {
		t.Fatal(err)
	}
	replacement := newController()
	t.Cleanup(replacement.ReleaseResources)
	old.goalResourceMu.Lock()
	old.goalTokensUsed = 340
	old.goalRequestsUsed = 4
	old.goalTokenLimit = 2000
	old.goalBudgetExtensions = 1
	old.goalResourceMu.Unlock()

	if err := replacement.InheritLifecycleFrom(old); err != nil {
		t.Fatal(err)
	}
	view, err := replacement.GetGoal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view == nil || view.Activation != goaldomain.ActivationArmed || view.Objective != "finish the rebuild" {
		t.Fatalf("replacement goal = %+v", view)
	}
	runtimeView := replacement.GoalRuntime()
	if runtimeView.TokensUsed != 340 || runtimeView.RequestsUsed != 4 || runtimeView.TokensLimit != 2000 || runtimeView.BudgetExtensions != 1 {
		t.Fatalf("replacement resource counters = %+v", runtimeView)
	}
}

func TestInheritLifecycleRejectsActiveGoalDriverReservation(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "goal-rebuild-busy"})
	if err != nil {
		t.Fatal(err)
	}
	newController := func() *Controller {
		exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
		return newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	}
	old := newController()
	replacement := newController()
	t.Cleanup(old.ReleaseResources)
	t.Cleanup(replacement.ReleaseResources)
	old.goalDriverMu.Lock()
	old.goalDriverPending = true
	old.goalDriverMu.Unlock()
	defer func() {
		old.goalDriverMu.Lock()
		old.goalDriverPending = false
		old.goalDriverMu.Unlock()
	}()

	if err := replacement.InheritLifecycleFrom(old); !errors.Is(err, session.ErrRuntimeBusy) {
		t.Fatalf("inherit error = %v, want runtime busy", err)
	}
}

func TestEditGoalDurablePreservesIdentityAndAdmittedRounds(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "goal-edit"})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	t.Cleanup(c.ReleaseResources)
	if err := c.SetGoalDurable("original objective"); err != nil {
		t.Fatal(err)
	}
	before, err := c.GetGoal(t.Context())
	if err != nil || before == nil {
		t.Fatalf("GetGoal before edit = %+v, %v", before, err)
	}
	if _, err := c.applyHostGoalMutation(t.Context(), "test-admit", func(machine *goaldomain.Machine) (*goaldomain.View, error) {
		admitted, admitErr := machine.AdmitRound(before.Ref())
		return &admitted, admitErr
	}); err != nil {
		t.Fatal(err)
	}
	limit := uint64(8)
	if err := c.EditGoalDurable("revised objective", &limit); err != nil {
		t.Fatal(err)
	}
	after, err := c.GetGoal(t.Context())
	if err != nil || after == nil {
		t.Fatalf("GetGoal after edit = %+v, %v", after, err)
	}
	if after.ID != before.ID || after.Revision != before.Revision+1 || after.RoundsStarted != 1 || after.Objective != "revised objective" || after.MaxGoalRounds == nil || *after.MaxGoalRounds != limit {
		t.Fatalf("edited goal = %+v, before = %+v", after, before)
	}
	if err := c.EditGoalDurable("invalid lower limit", new(uint64)); err == nil {
		t.Fatal("zero round limit unexpectedly accepted")
	}
	unchanged, _ := c.GetGoal(t.Context())
	if unchanged.Objective != after.Objective || unchanged.Revision != after.Revision || unchanged.RoundsStarted != after.RoundsStarted {
		t.Fatalf("failed edit changed goal: got %+v want %+v", unchanged, after)
	}
}
