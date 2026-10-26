package control

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	goaldomain "reasonix/internal/goal"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func goalWorkController(t *testing.T) (*Controller, *session.Runtime) {
	t.Helper()
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "goal-work"})
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	t.Cleanup(func() { setGoalWorkPhase(c, session.RuntimeIdle); c.Close() })
	setGoalWorkPhase(c, session.RuntimeRunning)
	_, err = c.applyHostGoalMutation(t.Context(), "test-create", func(machine *goaldomain.Machine) (*goaldomain.View, error) {
		view, err := machine.Create(goaldomain.CreateRequest{Objective: "finish work"})
		return &view, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, runtime
}

func setGoalWorkPhase(c *Controller, phase session.RuntimePhase) {
	c.mu.Lock()
	c.turns.phase = phase
	c.noteExecutionLocked(phase, "test-work")
	c.mu.Unlock()
}

func TestGoalWorkAccumulatesOnceAcrossPauseResumeAndDurableRestore(t *testing.T) {
	c, runtime := goalWorkController(t)
	first := c.beginGoalWork()
	if first == nil {
		t.Fatal("active user-triggered work was not measured")
	}
	_, err := c.applyHostGoalMutation(t.Context(), "test-pause", func(machine *goaldomain.Machine) (*goaldomain.View, error) {
		view, err := machine.Pause(machine.Get().Ref())
		return &view, err
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := c.finishGoalWork(first, first.started.Add(1200*time.Millisecond)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := c.GoalRuntime().WorkDurationMs; got != 1200 {
		t.Fatalf("paused work = %d", got)
	}
	if span := c.beginGoalWork(); span != nil {
		t.Fatal("paused goal started a work clock")
	}
	_, err = c.applyHostGoalMutation(t.Context(), "test-resume", func(machine *goaldomain.Machine) (*goaldomain.View, error) {
		view, err := machine.Resume(machine.Get().Ref(), true)
		return &view, err
	})
	if err != nil {
		t.Fatal(err)
	}
	second := c.beginGoalWork()
	if second == nil {
		t.Fatal("resumed goal did not start work")
	}
	// Separate Run clocks exclude an arbitrarily long gap between executions.
	second.started = first.started.Add(48 * time.Hour)
	_, err = c.applyHostGoalMutation(t.Context(), "test-complete", func(machine *goaldomain.Machine) (*goaldomain.View, error) {
		view, err := machine.Complete(machine.Get().Ref())
		return &view, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.finishGoalWork(second, second.started.Add(2300*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := c.GoalRuntime().WorkDurationMs; got != 3500 {
		t.Fatalf("completed work = %d", got)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	commits, err := runtime.Session().Handle().Read(t.Context(), 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	var restored *goaldomain.Machine
	for _, commit := range commits.Commits {
		for _, item := range commit.Events {
			if item.Kind == "goal/state" {
				restored, err = goalLifecycleFromProjection(item.Payload, runtime.Ref().SessionID, time.Time{})
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if restored == nil || restored.Get().WorkDurationMs != 3500 || restored.Get().Activation != goaldomain.ActivationDisarmed {
		t.Fatalf("durable restored goal = %+v", restored)
	}
}

func TestGoalWorkRejectsRetiredActivityAndReplacement(t *testing.T) {
	for _, transition := range []string{"replace", "clear", "activity", "recovery", "epoch", "runtime"} {
		t.Run(transition, func(t *testing.T) {
			c, runtime := goalWorkController(t)
			work := c.beginGoalWork()
			switch transition {
			case "replace", "clear":
				_, err := c.applyHostGoalMutation(t.Context(), "test-replace", func(machine *goaldomain.Machine) (*goaldomain.View, error) {
					if transition == "clear" {
						return nil, machine.Clear(machine.Get().Ref())
					}
					view, err := machine.Replace(goaldomain.CreateRequest{Objective: "replacement"})
					return &view, err
				})
				if err != nil {
					t.Fatal(err)
				}
			case "activity":
				work.token++
			case "epoch":
				work.epoch = "retired-epoch"
			case "runtime":
				work.runtime = nil
			case "recovery":
				runtime.RequireRecovery("cancel watchdog")
			}
			if err := c.finishGoalWork(work, work.started.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if got := c.GoalRuntime().WorkDurationMs; got != 0 {
				t.Fatalf("stale work = %d", got)
			}
		})
	}
}

type goalWorkErrorRunner struct{ err error }

func (r goalWorkErrorRunner) Run(context.Context, string) error { return r.err }

func TestGoalWorkRecordsCancelledAndFailedRuns(t *testing.T) {
	for _, runErr := range []error{context.Canceled, errors.New("provider failed")} {
		t.Run(runErr.Error(), func(t *testing.T) {
			c, _ := goalWorkController(t)
			c.runner = goalWorkErrorRunner{err: runErr}
			if err := c.runModelTurn(t.Context(), "work"); !errors.Is(err, runErr) {
				t.Fatalf("run error = %v", err)
			}
			if got := c.GoalRuntime().WorkDurationMs; got <= 0 {
				t.Fatalf("failed run work = %d", got)
			}
		})
	}
}

func TestGoalWorkLegacyProjectionPreservesDuration(t *testing.T) {
	machine, err := goalLifecycleFromProjection([]byte(`{"goal":"legacy","status":"stopped","turnsUsed":2,"workDurationMs":12345}`), "legacy", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got := machine.Get().WorkDurationMs; got != 12345 {
		t.Fatalf("legacy duration = %d", got)
	}
}

func TestGoalWorkPersistenceFailureDoesNotPublishDuration(t *testing.T) {
	c, runtime := goalWorkController(t)
	work := c.beginGoalWork()
	if err := runtime.Session().Handle().Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.finishGoalWork(work, work.started.Add(time.Second)); err == nil {
		t.Fatal("failed append was hidden")
	}
	if got := c.GoalRuntime().WorkDurationMs; got != 0 {
		t.Fatalf("uncommitted work published: %d", got)
	}
}

func TestGoalWorkDoesNotCountIdleOrDisarmedGoals(t *testing.T) {
	c, _ := goalWorkController(t)
	setGoalWorkPhase(c, session.RuntimeIdle)
	if work := c.beginGoalWork(); work != nil {
		t.Fatal("idle runtime started a clock")
	}
	setGoalWorkPhase(c, session.RuntimeRunning)
	c.disarmGoalLifecycle("cold-restore")
	if work := c.beginGoalWork(); work != nil {
		t.Fatal("disarmed goal started a clock")
	}
}
