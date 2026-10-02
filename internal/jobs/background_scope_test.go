package jobs

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"reasonix/internal/event"
)

type scopeEventSink struct{ emit func(event.Event) }

func (s scopeEventSink) Emit(e event.Event) { s.emit(e) }

func TestBackgroundScopeEventsSwitchInOrderAndAllowReentrantReads(t *testing.T) {
	var mu sync.Mutex
	var received []string
	m := NewManager(event.Discard)
	scope := NewSessionBackgroundScope(m, nil)
	defer scope.Release(false)
	delivered := make(chan struct{})
	release, err := m.BeginReplacement("")
	if err != nil {
		t.Fatal(err)
	}
	m.Emit(event.Event{Kind: event.Notice, Text: "one"})
	m.Emit(event.Event{Kind: event.Notice, Text: "two"})
	scope.Bind(scopeEventSink{func(e event.Event) {
		_ = m.Running() // The registry lock must not cross the callback.
		mu.Lock()
		received = append(received, e.Text)
		count := len(received)
		mu.Unlock()
		if count == 2 {
			close(delivered)
		}
	}}, nil)
	release()
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("buffered completion events were lost")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 || received[0] != "one" || received[1] != "two" {
		t.Fatalf("events: %v", received)
	}
}

func TestBackgroundScopeReplacementPreservesProcessAndCompletion(t *testing.T) {
	m := NewManager(event.Discard)
	scope := NewSessionBackgroundScope(m, nil)
	defer scope.Release(false)
	entered, finish := make(chan struct{}), make(chan struct{})
	j := m.StartSessionProcess("session", "bash", "gateway", func(ctx context.Context, out io.Writer) (string, error) {
		close(entered)
		select {
		case <-finish:
			return "completed", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	<-entered
	release, err := m.BeginReplacement("session")
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.Acquire(); err != nil {
		t.Fatal(err)
	}
	// Candidate disposal must not cancel the outgoing runtime's process.
	scope.Release(false)
	select {
	case <-j.done:
		t.Fatal("candidate disposal killed gateway")
	default:
	}
	blocked := m.StartForSession("session", "task", "late old-generation task", func(context.Context, io.Writer) (string, error) { t.Error("sealed task ran"); return "", nil })
	<-blocked.done
	release()
	if len(m.RunningForSession("session")) != 1 || len(m.BlockingJobs("session")) != 0 {
		t.Fatal("incorrect replacement classification")
	}
	close(finish)
	select {
	case <-j.done:
	case <-time.After(time.Second):
		t.Fatal("process did not finish")
	}
	if len(m.RunningForSession("session")) != 0 {
		t.Fatal("finished process remained active")
	}
}

func TestBackgroundScopeRuntimeTaskBlocksUntilActuallyExited(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()
	entered, unwind := make(chan struct{}), make(chan struct{})
	j := m.StartForSession("session", "task", "dependent", func(ctx context.Context, _ io.Writer) (string, error) {
		close(entered)
		<-ctx.Done()
		<-unwind
		return "", ctx.Err()
	})
	<-entered
	m.Kill(j.ID)
	if release, err := m.BeginReplacement("session"); err == nil {
		release()
		t.Error("cancelled-but-running task admitted replacement")
	}
	close(unwind)
	<-j.done
	release, err := m.BeginReplacement("session")
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestBackgroundScopeLastRuntimeReleaseCancelsProcesses(t *testing.T) {
	m := NewManager(event.Discard)
	scope := NewSessionBackgroundScope(m, nil)
	j := m.StartSessionProcess("session", "bash", "gateway", func(ctx context.Context, _ io.Writer) (string, error) { <-ctx.Done(); return "", ctx.Err() })
	if err := scope.Acquire(); err != nil {
		t.Fatal(err)
	}
	scope.Release(false)
	select {
	case <-j.done:
		t.Fatal("old generation killed process")
	default:
	}
	scope.Release(false)
	select {
	case <-j.done:
	case <-time.After(time.Second):
		t.Fatal("final release did not cancel")
	}
	if err := scope.Acquire(); err == nil {
		t.Fatal("closed scope was resurrected")
	}
}

func TestBackgroundScopeResamplesExitSuppressedDuringReplacement(t *testing.T) {
	m := NewManager(event.Discard)
	defer m.Close()
	finish := make(chan struct{})
	j := m.StartSessionProcess("session", "bash", "gateway", func(ctx context.Context, _ io.Writer) (string, error) {
		select {
		case <-finish:
			return "", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	release, err := m.BeginReplacement("session")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	suppressed, published := make(chan struct{}, 1), make(chan struct{}, 1)
	_, unsubscribe := m.SubscribeRuntime("session", func(state RuntimeState) {
		if state.Running != 0 {
			return
		}
		target := published
		if m.ReplacementInProgress() {
			target = suppressed
		}
		select {
		case target <- struct{}{}:
		default:
		}
	})
	defer unsubscribe()
	close(finish)
	<-j.done
	select {
	case <-suppressed:
	case <-time.After(time.Second):
		t.Fatal("completion did not reach sealed observer")
	}
	release()
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("publication lost the process exit suppressed during build")
	}
}
