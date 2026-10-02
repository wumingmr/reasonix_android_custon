package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"reasonix/internal/session"
)

type busyShutdownExecution struct{}

func (busyShutdownExecution) Snapshot() session.RuntimeSnapshot { return session.RuntimeSnapshot{} }
func (busyShutdownExecution) Cancel() bool                      { return false }

func TestShutdownServiceFailurePreservesEvidenceAndRetries(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	tracker := lifecycleTrackerForTest(t, t.TempDir(), 4242, "busy-service")
	if err := tracker.start(); err != nil {
		t.Fatal(err)
	}
	app.lifecycle.tracker = tracker
	service := app.desktopSessionService("")
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "busy-service"})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := service.Bind(runtime)
	if err != nil {
		t.Fatal(err)
	}
	generation := runtime.BindExecution(busyShutdownExecution{})
	if generation == 0 || !runtime.BeginExecution(generation, "test") {
		t.Fatal("could not create busy runtime")
	}
	defer func() {
		runtime.NoteExecution(generation, session.RuntimeIdle, "")
		runtime.UnbindExecution(generation)
		_ = binding.Release(context.Background())
		_ = service.Shutdown(context.Background())
		tracker.stopWriter()
	}()
	request := shutdownRequest{RequestID: "busy-service", Reason: shutdownReasonUserQuit}
	failed, err := app.requestShutdown(context.Background(), request)
	if !errors.Is(err, session.ErrRuntimeBusy) || failed.Completed || !failed.Retryable || failed.ErrorCode != "session_service_close_failed" {
		t.Fatalf("busy shutdown = %+v, %v", failed, err)
	}
	state, err := readDesktopLifecycleState(tracker.path)
	if err != nil || state.CleanupOutcome != "failed" {
		t.Fatalf("failure evidence was lost: %+v %v", state, err)
	}
	if app.shutdownState().finished["session-services"] {
		t.Fatal("failed service was checkpointed as completed")
	}
	runtime.NoteExecution(generation, session.RuntimeIdle, "")
	completed, err := app.requestShutdown(context.Background(), request)
	if err != nil || !completed.Completed || completed.Outcome != "success" {
		t.Fatalf("retry = %+v, %v", completed, err)
	}
	if _, retained := service.Runtime(runtime.Ref()); retained {
		t.Fatal("successful retry retained runtime")
	}
	if _, err := os.Stat(tracker.path); !os.IsNotExist(err) {
		t.Fatalf("completed user exit retained failure marker: %v", err)
	}
}

func TestReleasedBindingWarningDoesNotHideOtherShutdownErrors(t *testing.T) {
	bound := fmt.Errorf("released: %w", session.ErrRuntimeBound)
	if !onlyReleasedBindingErrors(errors.Join(bound, bound)) {
		t.Fatal("released binding warnings should not make shutdown fail permanently")
	}
	for _, err := range []error{session.ErrRuntimeBusy, errors.Join(bound, session.ErrRuntimeBusy), errors.Join(bound, errors.New("disk failure"))} {
		if onlyReleasedBindingErrors(err) {
			t.Fatalf("real cleanup failure swallowed: %v", err)
		}
	}
}
