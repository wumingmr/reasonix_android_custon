package control

import (
	"context"
	"io"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/jobs"
)

func TestModelReplacementFencesCandidateAndRetiredCallbacks(t *testing.T) {
	isolateControlConfigHome(t)
	m := jobs.NewManager(event.Discard)
	scope := jobs.NewSessionBackgroundScope(m, nil)
	old := newOwnedTestController(t, Options{Jobs: m, BackgroundScope: scope})
	old.SetToolApprovalMode(ToolApprovalYolo)
	process := m.StartSessionProcess("", "bash", "gateway", func(ctx context.Context, _ io.Writer) (string, error) { <-ctx.Done(); return "", ctx.Err() })
	if ModelReplacementBlocked(old) {
		t.Fatal("independent process blocked model replacement")
	}
	_, finish, abort, err := ReserveBackgroundReplacement(old)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	if err := scope.Acquire(); err != nil {
		t.Fatal(err)
	}
	next := newOwnedTestController(t, Options{Jobs: m, BackgroundScope: scope})
	if err := finish(next); err != nil {
		t.Fatal(err)
	}
	// Candidate setup can narrow its initial mode before migration restores the
	// old mode. It must not cancel jobs still owned by the outgoing controller.
	next.SetToolApprovalMode(ToolApprovalYolo)
	next.SetToolApprovalMode(ToolApprovalAsk)
	if len(m.Running()) != 1 {
		t.Fatal("candidate initialization cancelled outgoing process")
	}
	if old.receivesBackgroundRuntimeEvents() || next.receivesBackgroundRuntimeEvents() {
		t.Fatal("replacement published background callbacks before activation")
	}
	if err := ActivateControllerReplacement(old, next); err == nil {
		t.Fatal("permission-changing replacement kept background process")
	}
	next.SetToolApprovalMode(old.ToolApprovalMode())
	if err := ActivateControllerReplacement(old, next); err != nil {
		t.Fatal(err)
	}
	if old.receivesBackgroundRuntimeEvents() || !next.receivesBackgroundRuntimeEvents() {
		t.Fatal("background callbacks did not transfer to published runtime")
	}
	if len(m.Running()) != 1 || m.Running()[0].ID != process.ID {
		t.Fatal("activation replaced gateway identity")
	}
}

func TestModelApplicationCancelOnlySelectedRuntimeTasks(t *testing.T) {
	isolateControlConfigHome(t)
	m := jobs.NewManager(event.Discard)
	scope := jobs.NewSessionBackgroundScope(m, nil)
	c := newOwnedTestController(t, Options{Jobs: m, BackgroundScope: scope})
	start := func(process bool, cancelled chan struct{}) *jobs.Job {
		run := func(ctx context.Context, _ io.Writer) (string, error) {
			<-ctx.Done()
			close(cancelled)
			return "", ctx.Err()
		}
		if process {
			return m.StartSessionProcess("", "bash", "gateway", run)
		}
		return m.StartForSession("", "task", "dependent", run)
	}
	selectedExit, otherExit, processExit := make(chan struct{}), make(chan struct{}), make(chan struct{})
	selected, other, process := start(false, selectedExit), start(false, otherExit), start(true, processExit)
	if !ModelReplacementBlocked(c) {
		t.Fatal("runtime task did not block replacement")
	}
	c.CancelModelApplicationBlockers([]string{selected.ID, process.ID, "missing"})
	<-selectedExit
	for _, exit := range []chan struct{}{otherExit, processExit} {
		select {
		case <-exit:
			t.Fatal("unselected or session process cancelled")
		default:
		}
	}
	if len(c.ModelReplacementJobs()) == 0 {
		t.Fatalf("remaining task %s lost", other.ID)
	}
}
