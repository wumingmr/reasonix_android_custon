package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/desktop/internal/sessionui"
)

func TestManualCreationManagerProcess(t *testing.T) {
	path := os.Getenv("REASONIX_TEST_CREATION_STORE")
	if path == "" {
		return
	}
	a := NewApp()
	a.ctx = t.Context()
	installNoopRuntimeEvents(a)
	a.sessionUI = sessionui.New(path)
	m := a.creationManager()
	m.execute = func(context.Context, ManualSessionCreationView, func(string)) error {
		fmt.Println("executing")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		return nil
	}
	m.Ensure("two-process-creation", "recovery", "")
	a.manualCreationTasks.Wait()
	if err := a.stopManualCreations(); err != nil {
		t.Fatal(err)
	}
	_ = a.sessionUI.Close()
}

func TestManualCreationTwoManagersRespectCommittedResult(t *testing.T) {
	a := newManualSessionTestApp(t)
	v, _ := seedManualCreation(t, a, "two-process-creation", "starting")
	cmd := exec.Command(os.Args[0], "-test.run=^TestManualCreationManagerProcess$")
	cmd.Env = append(os.Environ(), "REASONIX_TEST_CREATION_STORE="+a.sessionUIStore().Path())
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "executing\n" {
		t.Fatalf("owner handshake: %q %v", line, err)
	}
	m := a.creationManager()
	var executions atomic.Int32
	m.execute = func(context.Context, ManualSessionCreationView, func(string)) error { executions.Add(1); return nil }
	m.Ensure(v.OperationID, "recovery", "")
	waitFor(t, "second manager waits", func() bool { p := m.Snapshot(v.OperationID); return p != nil && p.Status == "waiting_lock" })
	_ = input.Close()
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	a.manualCreationTasks.Wait()
	got, err := a.GetManualSessionCreation(v.OperationID)
	if err != nil || got.Phase != "ready" || executions.Load() != 0 {
		t.Fatalf("competing execution: %+v count=%d err=%v", got, executions.Load(), err)
	}
}

func TestManualCreationStaleRetryCannotRestartNewerFailure(t *testing.T) {
	a := newManualSessionTestApp(t)
	v, record := seedManualCreation(t, a, "stale-retry-creation", "failed")
	if _, err := a.sessionUIStore().Save(t.Context(), "creation", record.Key, record.Revision, record.Payload); err != nil {
		t.Fatal(err)
	}
	m := a.creationManager()
	var executions atomic.Int32
	m.execute = func(context.Context, ManualSessionCreationView, func(string)) error { executions.Add(1); return nil }
	m.Ensure(v.OperationID, "retry", record.Revision)
	a.manualCreationTasks.Wait()
	if executions.Load() != 0 {
		t.Fatal("stale request restarted a newer failure")
	}
	if _, err := a.RetryManualSessionCreation(v.OperationID); err != nil {
		t.Fatal(err)
	}
	a.manualCreationTasks.Wait()
	if executions.Load() != 1 {
		t.Fatal("fresh explicit retry did not execute")
	}
}

func TestManualCreationSlowProgressIsObservationOnly(t *testing.T) {
	a := &App{}
	m := &manualCreationManager{a: a, tasks: make(map[string]*manualCreationTask), now: func() time.Time { return time.Unix(100, 0) }}
	task := &manualCreationTask{id: "slow-operation", progress: ManualCreationProgress{Status: "running", Stage: "building_runtime", StageStartedAt: 1}}
	m.tasks[task.id] = task
	p := m.Snapshot(task.id)
	if !p.Slow || p.Status != "running" || len(m.tasks) != 1 {
		t.Fatalf("slow status triggered a transition: %+v", p)
	}
}

func TestManualCreationLateStageCannotReplaceNewerGeneration(t *testing.T) {
	m := &manualCreationManager{tasks: make(map[string]*manualCreationTask), now: time.Now}
	task := &manualCreationTask{id: "late-stage", running: true}
	m.tasks[task.id] = task
	m.setStageForGeneration(task, "running", "building_runtime", 2)
	m.setStageForGeneration(task, "running", "publishing_controller", 1)
	if got := m.Snapshot(task.id); got.Stage != "building_runtime" {
		t.Fatalf("late stage replaced generation 2: %+v", got)
	}
}
