package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/identitylock"
)

func TestManualCreationWaitsForSupersededBuildActualExit(t *testing.T) {
	a := newManualSessionTestApp(t)
	entered := make(chan struct{}, 2)
	first, second := make(chan struct{}), make(chan struct{})
	var firstOnce, secondOnce sync.Once
	defer firstOnce.Do(func() { close(first) })
	defer secondOnce.Do(func() { close(second) })
	var calls atomic.Int32
	a.tabBuildStartHook = func(string) {
		n := calls.Add(1)
		entered <- struct{}{}
		if n == 1 {
			<-first
		} else {
			<-second
		}
	}
	v, err := a.BeginManualSessionCreation(ManualSessionCreationRequest{OperationID: "superseded-creation-owner", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	a.mu.RLock()
	var tab *WorkspaceTab
	for _, candidate := range a.tabs {
		if candidate.SessionID == v.Ref.SessionID {
			tab = candidate
		}
	}
	old := tab.buildExecution
	oldNotification := tab.buildDone
	a.mu.RUnlock()
	a.startTabControllerBuild(tab)
	<-entered
	select {
	case <-oldNotification:
	default:
		t.Fatal("test did not supersede the notification")
	}
	m := a.creationManager()
	a.shuttingDown.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err = m.CancelAndWait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unjoined build stopped: %v", err)
	}
	firstOnce.Do(func() { close(first) })
	<-old.done
	path := filepath.Join(filepath.Dir(a.sessionUIStore().Path()), "manual-creation-locks", v.Ref.SessionID+".lock")
	unlock, err := identitylock.TryAcquire(path)
	if err == nil {
		unlock()
		t.Fatal("released before the second build exited")
	}
	if !errors.Is(err, identitylock.ErrHeld) {
		t.Fatal(err)
	}
	secondOnce.Do(func() { close(second) })
	if err = m.CancelAndWait(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _ := a.GetManualSessionCreation(v.OperationID)
	if got.Phase != "starting" {
		t.Fatalf("cancelled attempt persisted a terminal result: %+v", got)
	}
}

func TestManualCreationResumesPreparedStorage(t *testing.T) {
	a := newManualSessionTestApp(t)
	v, _ := seedManualCreation(t, a, "prepared-creation-recovery", "starting")
	tab, err := a.reserveManualSessionTab(t.Context(), v, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	m := a.creationManager()
	m.Ensure(v.OperationID, "recovery", "")
	a.manualCreationTasks.Wait()
	got, err := a.GetManualSessionCreation(v.OperationID)
	if err != nil || got.Phase != "ready" || got.Ref != v.Ref {
		t.Fatalf("resume: %+v %v", got, err)
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.tabs) != 1 || a.tabs[tab.ID] != tab {
		t.Fatal("prepared runtime identity was replaced")
	}
}
