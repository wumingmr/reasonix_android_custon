package control

import (
	"context"
	"errors"
	"testing"
	"time"

	goaldomain "reasonix/internal/goal"
	"reasonix/internal/hook"
	"reasonix/internal/session"
)

func TestGoalWorkPinsAppendDestinationAcrossBindingChange(t *testing.T) {
	c, original := goalWorkController(t)
	service, _, _ := c.v3Binding()
	replacement, err := service.Create(t.Context(), session.CreateOptions{SessionID: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { c.v3BindingMu.Lock(); c.sessionRuntime = original; c.v3BindingMu.Unlock() }()
	_, err = c.applyHostGoalMutationForRuntime(t.Context(), "work-duration", original, func(machine *goaldomain.Machine) (*goaldomain.View, error) {
		view, err := machine.AddWorkDuration(machine.Get().Ref(), 1000)
		// Rebind after the candidate is prepared but before its destination is read.
		c.v3BindingMu.Lock()
		c.sessionRuntime = replacement
		c.v3BindingMu.Unlock()
		return &view, err
	})
	if !errors.Is(err, errStaleGoalWork) {
		t.Fatalf("stale binding error = %v", err)
	}
	if raw := replacement.Session().StateSnapshot().Projection.GoalState; len(raw) != 0 {
		t.Fatalf("old goal written to replacement: %s", raw)
	}
	if got := c.GoalRuntime().WorkDurationMs; got != 0 {
		t.Fatalf("stale work published = %d", got)
	}
}

func TestGoalWorkSurvivesCancellationActivityRevision(t *testing.T) {
	c, runtime := goalWorkController(t)
	work := c.beginGoalWork()
	before := runtime.StateSnapshot().ActivityRevision
	_, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	close(done)
	c.mu.Lock()
	c.turns.cancel = cancel
	c.turns.done = done
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.turns.cancel, c.turns.done = nil, nil
		c.mu.Unlock()
	}()
	if !runtime.Cancel() {
		t.Fatal("runtime did not cancel")
	}
	snapshot := runtime.StateSnapshot()
	if snapshot.ActivityRevision == before || snapshot.Phase != session.RuntimeCancelling {
		t.Fatalf("cancellation did not change runtime revision: %+v", snapshot)
	}
	if err := c.finishGoalWork(work, work.started.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := c.GoalRuntime().WorkDurationMs; got != 1000 {
		t.Fatalf("cancelled work = %d", got)
	}
}

func TestGoalWorkDoesNotCountRejectedSubagentSubmission(t *testing.T) {
	c, _ := goalWorkController(t)
	called := false
	c.hooks = hook.NewRunner([]hook.ResolvedHook{{
		HookConfig: hook.HookConfig{Command: "reject"}, Event: hook.UserPromptSubmit,
	}}, t.TempDir(), func(context.Context, hook.SpawnInput) hook.SpawnResult {
		called = true
		return hook.SpawnResult{ExitCode: 2, Stderr: "rejected"}
	}, nil)
	err := newTurnOrchestrator(c).runSubagentSkillTurns(t.Context(), nil, "work", "work", "work", nil, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("submission gate was not exercised")
	}
	if got := c.GoalRuntime().WorkDurationMs; got != 0 {
		t.Fatalf("rejected submission work = %d", got)
	}
}
