package control

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestCancelSessionSignalsAndAcknowledgesWhileSamplingBlocked(t *testing.T) {
	isolateControlConfigHome(t)
	c := newOwnedTestController(t, Options{SessionDir: t.TempDir()})
	defer c.Close()
	started, cancelled, releaseBody := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(releaseBody)
	c.runGuarded(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-releaseBody
		return ctx.Err()
	})
	<-started
	c.runtimeState.mu.Lock()
	unlock := sync.OnceFunc(c.runtimeState.mu.Unlock)
	defer unlock()
	returned := make(chan CancelReceipt, 1)
	go func() { returned <- c.CancelSessionFrom("test") }()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop waited for state observation before signalling execution")
	}
	select {
	case receipt := <-returned:
		if !receipt.Accepted || receipt.AlreadyIdle || receipt.RuntimeEpoch == "" {
			t.Fatalf("incorrect cancellation receipt: %+v", receipt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop receipt waited for state observation")
	}
}

func TestMaintenanceCancelAcknowledgesWhilePublicationBlocked(t *testing.T) {
	isolateControlConfigHome(t)
	c := newOwnedTestController(t, Options{SessionDir: t.TempDir()})
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	op := &controllerMaintenance{maintenanceIdentity: maintenanceIdentity{id: "cancel-test"}, activity: "running", cancel: cancel, done: make(chan struct{})}
	c.mu.Lock()
	c.maintenance = op
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.maintenance = nil
		close(op.done)
		c.mu.Unlock()
	}()
	op.publishMu.Lock()
	defer op.publishMu.Unlock()
	returned := make(chan CancelReceipt, 1)
	go func() { returned <- c.CancelSessionFrom("test") }()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance cancellation did not signal its owner")
	}
	select {
	case receipt := <-returned:
		if !receipt.Accepted || receipt.AlreadyIdle {
			t.Fatalf("maintenance receipt = %+v", receipt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance receipt waited for publication")
	}
}
