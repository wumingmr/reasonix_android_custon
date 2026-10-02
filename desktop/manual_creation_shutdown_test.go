package main

import (
	"context"
	"sync"
	"testing"
)

func TestManualCreationShutdownFailureRetainsResourcesAndRetries(t *testing.T) {
	a := newManualSessionTestApp(t)
	tracker := lifecycleTrackerForTest(t, t.TempDir(), 4243, "manual-creation-stop")
	if err := tracker.start(); err != nil {
		t.Fatal(err)
	}
	a.lifecycle.tracker = tracker
	t.Cleanup(tracker.stopWriter)
	v, _ := seedManualCreation(t, a, "shutdown-creation-owner", "starting")
	m := a.creationManager()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	m.execute = func(context.Context, ManualSessionCreationView, func(string)) error {
		close(entered)
		<-release
		return nil
	}
	m.Ensure(v.OperationID, "recovery", "")
	<-entered
	request := shutdownRequest{RequestID: "manual-creation-stop", Reason: shutdownReasonUserQuit}
	status, err := a.requestShutdown(context.Background(), request)
	if err == nil || status.Completed || !status.Retryable || status.ErrorCode != "manual_creation_stop_timeout" {
		t.Fatalf("shutdown=%+v %v", status, err)
	}
	coordinator := a.shutdownState()
	coordinator.mu.Lock()
	closed := coordinator.finished["session-ui"] || coordinator.finished["manual-session-creation"]
	coordinator.mu.Unlock()
	if closed {
		t.Fatal("unfinished teardown was checkpointed")
	}
	if _, err := a.sessionUIStore().Get(t.Context(), "creation", v.OperationID); err != nil {
		t.Fatal("database closed before worker termination", err)
	}
	once.Do(func() { close(release) })
	status, err = a.requestShutdown(context.Background(), request)
	if err != nil || !status.Completed {
		t.Fatalf("retry=%+v %v", status, err)
	}
}
