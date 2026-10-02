package control

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

func TestControllerCloseCancelsGoalDriverFlushWait(t *testing.T) {
	flushStarted := make(chan struct{})
	releaseFlush := make(chan struct{})
	var once sync.Once
	store, err := session.CreateWithOptions(t.TempDir()+"/goal-close-flush", "goal-close-flush", session.OpenOptions{
		Sync: func(*os.File) error {
			once.Do(func() { close(flushStarted) })
			<-releaseFlush
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := session.NewService("desktop", failingFlushPersistence{session: store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "goal-close-flush"})
	if err != nil {
		t.Fatal(err)
	}
	replacementBinding, err := service.Bind(runtime)
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	if err := c.SetGoalDurable("close without waiting for a stuck disk"); err != nil {
		t.Fatal(err)
	}
	c.kickGoalDriver()
	select {
	case <-flushStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("goal driver did not enter Flush")
	}
	done := make(chan struct{})
	go func() {
		c.ReleaseResources()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(releaseFlush)
		t.Fatal("controller close remained blocked on the goal driver Flush")
	}
	close(releaseFlush)
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := replacementBinding.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
