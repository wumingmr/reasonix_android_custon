package control

import (
	"context"
	"testing"
	"time"

	"reasonix/internal/session"
)

func TestRecoveryCloseWaitsForTerminalFanout(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	c := newOwnedTestController(t, Options{Sink: holdFinishingWindow(release, entered, nil)})
	// Always release the sink before the controller cleanup joins it.
	t.Cleanup(func() { close(release) })
	started := make(chan struct{})
	body := make(chan struct{})
	c.runGuarded(func(context.Context) error {
		close(started)
		<-body
		return nil
	})
	<-started
	// Force the state published by the cancellation watchdog, then hold the
	// recovered body's terminal fanout while Close races its completion.
	c.mu.Lock()
	c.turns.phase = session.RuntimeRecoveryRequired
	c.mu.Unlock()
	close(body)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery terminal fanout did not start")
	}
	c.Close()
	select {
	case <-c.closeFinalized:
		t.Fatal("controller resources closed while recovery terminal fanout was active")
	default:
	}
}

func TestRecoveryCloseWaitsForWatchdogFanout(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	c := newOwnedTestController(t, Options{Sink: holdFinishingWindow(release, entered, nil)})
	t.Cleanup(func() { close(release) })
	c.testCancelGrace = time.Millisecond
	started := make(chan struct{})
	body := make(chan struct{})
	c.runGuarded(func(ctx context.Context) error {
		close(started)
		<-body
		return ctx.Err()
	})
	<-started
	idle, _ := c.TurnIdleDone()
	c.CancelSession()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(body)
		t.Fatal("watchdog terminal fanout did not start")
	}
	c.Close()
	close(body)
	select {
	case <-idle:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery body did not settle")
	}
	select {
	case <-c.closeFinalized:
		t.Fatal("controller resources closed while watchdog terminal fanout was active")
	default:
	}
}
