package control

import (
	"path/filepath"
	"testing"

	"reasonix/internal/session"
)

func TestSetPlanModeSameValueDoesNotAppendDomainState(t *testing.T) {
	runner := &planModeCountingRunner{}
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "plan-idempotent"})
	if err != nil {
		t.Fatal(err)
	}
	c := newOwnedTestController(t, Options{Runner: runner, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	_, runtime, ok := c.SessionBinding()
	if !ok {
		t.Fatal("controller has no canonical session binding")
	}
	before := runtime.StateSnapshot().Session.EventSequence
	c.SetPlanMode(false)
	if after := runtime.StateSnapshot().Session.EventSequence; after != before {
		t.Fatalf("same plan mode advanced event sequence: before=%d after=%d", before, after)
	}
	if runner.calls != 1 || runner.last {
		t.Fatalf("same plan mode propagation calls=%d last=%v, want 1/false", runner.calls, runner.last)
	}
}
