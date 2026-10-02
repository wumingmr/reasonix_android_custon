package control

import (
	"context"
	"errors"
	"os"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func TestGoalCommandDoesNotStartProviderAfterPersistenceFailure(t *testing.T) {
	store, err := session.CreateWithOptions(t.TempDir()+"/goal-command-failure", "goal-command-failure", session.OpenOptions{
		Sync: func(*os.File) error { return errors.New("injected sync failure") },
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := session.NewService("desktop", failingFlushPersistence{session: store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "goal-command-failure"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().AppendBatch(t.Context(), "seed", []session.Event{{Kind: "tool/result", Payload: []byte(`{"id":"seed-call","name":"bash","output":"seed"}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err == nil {
		t.Fatal("seed Flush unexpectedly succeeded")
	}
	runner := &modelErrorGoalRunner{}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Runner: runner, Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	t.Cleanup(c.ReleaseResources)

	c.Submit("/goal ship the durable fix")
	if runner.calls != 0 {
		t.Fatalf("provider calls = %d, want zero", runner.calls)
	}
	if snapshot := runtime.Session().Snapshot(); snapshot.PersistenceStatus != session.PersistenceUncertain {
		t.Fatalf("persistence status = %q, want uncertain", snapshot.PersistenceStatus)
	}
}
