package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/desktop/internal/sessionui"
)

type manualCreationScanStore struct {
	manualCreationStore
	reads  chan struct{}
	failed atomic.Bool
}

func (s *manualCreationScanStore) List(ctx context.Context, kind string) ([]sessionui.Record, error) {
	if !s.failed.Swap(true) {
		s.reads <- struct{}{}
		return nil, creationBusyError{}
	}
	return s.manualCreationStore.List(ctx, kind)
}

func TestManualCreationPeriodicScanRecoversAfterReadFailure(t *testing.T) {
	a := newManualSessionTestApp(t)
	v, _ := seedManualCreation(t, a, "periodic-recovery-intent", "starting")
	m := a.creationManager()
	store := &manualCreationScanStore{manualCreationStore: m.store, reads: make(chan struct{}, 1)}
	m.store = store
	m.execute = func(context.Context, ManualSessionCreationView, func(string)) error { return nil }
	var offset atomic.Int64
	m.mu.Lock()
	m.now = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	m.mu.Unlock()
	m.armRecovery()
	<-store.reads
	// Advance the injected scheduler clock and wake it; no wall-clock sleep is
	// needed to exercise the thirty-second rescan contract.
	offset.Store(int64(31 * time.Second))
	m.notify()
	waitFor(t, "periodic recovery", func() bool { got, _ := a.GetManualSessionCreation(v.OperationID); return got.Phase == "ready" })
}

func TestManualCreationDuplicateBeginRegistersOrphanedIntent(t *testing.T) {
	a := newManualSessionTestApp(t)
	v, _ := seedManualCreation(t, a, "duplicate-begin-recovery", "starting")
	got, err := a.BeginManualSessionCreation(ManualSessionCreationRequest{OperationID: v.OperationID, Scope: "global"})
	if err != nil || got.Ref != v.Ref {
		t.Fatalf("begin=%+v %v", got, err)
	}
	a.manualCreationTasks.Wait()
	got, err = a.GetManualSessionCreation(v.OperationID)
	if err != nil || got.Phase != "ready" {
		t.Fatalf("orphan not recovered: %+v %v", got, err)
	}
}
