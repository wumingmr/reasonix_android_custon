package session

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type testExecution struct {
	mu      sync.Mutex
	phase   RuntimePhase
	cancel  context.CancelFunc
	ctx     context.Context
	gen     uint64
	runtime *Runtime
}

type blockingRejectExecution struct {
	entered chan<- struct{}
	release <-chan struct{}
}

type advancingCancelExecution struct {
	*testExecution
	advance func()
}

func (e *advancingCancelExecution) Cancel() bool { e.advance(); return true }

func TestMaintenanceCancelAcknowledgementCannotCancelSuccessor(t *testing.T) {
	_, runtime := reviewRuntime(t)
	exec := &advancingCancelExecution{testExecution: &testExecution{runtime: runtime}}
	exec.gen = runtime.BindExecution(exec)
	if !runtime.BeginExecution(exec.gen, MaintenanceActivity) {
		t.Fatal("maintenance admission failed")
	}
	exec.advance = func() {
		if !runtime.FinishMaintenanceExecution(exec.gen) || !runtime.BeginExecution(exec.gen, "turn") {
			t.Fatal("could not hand off to queued turn")
		}
	}
	if !runtime.Cancel() {
		t.Fatal("cancellation was not acknowledged")
	}
	if got := runtime.StateSnapshot(); got.Phase != RuntimeRunning || got.Activity != "turn" {
		t.Fatalf("late acknowledgement changed successor: %+v", got)
	}
	runtime.NoteExecution(exec.gen, RuntimeIdle, "")
	runtime.UnbindExecution(exec.gen)
}

func (e *blockingRejectExecution) Snapshot() RuntimeSnapshot {
	return RuntimeSnapshot{Phase: RuntimeIdle}
}
func (e *blockingRejectExecution) Cancel() bool {
	e.entered <- struct{}{}
	<-e.release
	return false
}

func bindTestExecution(t *testing.T, runtime *Runtime, name string) (context.Context, *testExecution) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	exec := &testExecution{phase: RuntimeRunning, cancel: cancel, ctx: ctx, gen: 1, runtime: runtime}
	exec.gen = runtime.BindExecution(exec)
	runtime.NoteExecution(exec.gen, RuntimeRunning, name)
	t.Cleanup(exec.Finish)
	return ctx, exec
}

func (e *testExecution) Snapshot() RuntimeSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return RuntimeSnapshot{Phase: e.phase}
}

func (e *testExecution) Cancel() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.phase != RuntimeRunning && e.phase != RuntimeCancelling {
		return false
	}
	e.phase = RuntimeCancelling
	if e.cancel != nil {
		e.cancel()
	}
	return true
}

func (e *testExecution) Finish() {
	e.mu.Lock()
	if e.phase == RuntimeIdle || e.phase == RuntimeClosed {
		e.mu.Unlock()
		return
	}
	e.phase = RuntimeIdle
	e.mu.Unlock()
	if e.runtime != nil {
		e.runtime.NoteExecution(e.gen, RuntimeIdle, "")
	}
}

func TestBindExecutionDoesNotSilentlyReplaceExistingOwner(t *testing.T) {
	_, runtime := reviewRuntime(t)
	first := &testExecution{phase: RuntimeRunning, runtime: runtime}
	first.gen = runtime.BindExecution(first)
	runtime.NoteExecution(first.gen, RuntimeRunning, "first")

	second := &testExecution{phase: RuntimeRunning, runtime: runtime}
	if gen := runtime.BindExecution(second); gen != 0 {
		t.Fatalf("second bind generation = %d, want rejection", gen)
	}
	if !runtime.Cancel() {
		t.Fatal("cancel did not reach the original execution owner")
	}
	if got := first.phase; got != RuntimeCancelling {
		t.Fatalf("original owner phase = %s, want cancelling", got)
	}
	// A runtime with a live execution refuses to close, which would strand its
	// writer lease past the test.
	first.Finish()
}

func TestUnbindDoesNotClearNewerExecutionGeneration(t *testing.T) {
	_, runtime := reviewRuntime(t)
	first := &testExecution{phase: RuntimeRunning, runtime: runtime}
	second := &testExecution{phase: RuntimeRunning, runtime: runtime}
	first.gen = runtime.BindExecution(first)
	second.gen = runtime.ReplaceExecution(first.gen, second)
	if second.gen == 0 {
		t.Fatal("replace idle execution")
	}
	runtime.NoteExecution(second.gen, RuntimeRunning, "new")
	runtime.UnbindExecution(first.gen)
	if !runtime.Cancel() {
		t.Fatal("new generation lost cancel after old unbind")
	}
	if got := runtime.StateSnapshot().Phase; got != RuntimeCancelling {
		t.Fatalf("phase = %s, want cancelling", got)
	}
	runtime.NoteExecution(first.gen, RuntimeIdle, "")
	if got := runtime.StateSnapshot().Phase; got != RuntimeCancelling {
		t.Fatalf("old generation cleared new phase: %s", got)
	}
	second.Finish()
}

func TestSessionAcceptsCancelHistoryWhileCancelling(t *testing.T) {
	_, runtime := reviewRuntime(t)
	_, exec := bindTestExecution(t, runtime, "turn")
	if !runtime.Cancel() {
		t.Fatal("cancel")
	}
	payload := []byte(`{"messages":[],"reason":"cancel-or-recovery-rewrite"}`)
	if _, err := runtime.Session().Append(t.Context(), Batch{
		OperationID: "history-replace",
		TurnID:      "turn-1",
		Events:      []Event{{Kind: "history/replace", Payload: payload}},
	}); err != nil {
		t.Fatalf("history/replace during cancel: %v", err)
	}
	if _, err := runtime.Session().Append(t.Context(), Batch{
		OperationID: "turn-end",
		TurnID:      "turn-1",
		Events:      []Event{{Kind: "turn/end", Payload: []byte(`{"status":"interrupted"}`)}},
	}); err != nil {
		t.Fatalf("turn/end during cancel: %v", err)
	}
	exec.Finish()
	if got := runtime.StateSnapshot().Phase; got != RuntimeIdle {
		t.Fatalf("phase after finish = %s", got)
	}
}

func TestOldControllerCannotIdleNewGeneration(t *testing.T) {
	_, runtime := reviewRuntime(t)
	old := &testExecution{phase: RuntimeRunning, runtime: runtime}
	old.gen = runtime.BindExecution(old)
	next := &testExecution{phase: RuntimeIdle, runtime: runtime}
	next.gen = runtime.ReplaceExecution(old.gen, next)
	if next.gen == 0 {
		t.Fatal("replace idle execution")
	}
	runtime.NoteExecution(next.gen, RuntimeRunning, "new")
	runtime.NoteExecution(old.gen, RuntimeIdle, "")
	if got := runtime.StateSnapshot().Phase; got != RuntimeRunning {
		t.Fatalf("old finish cleared new turn: %s", got)
	}
	// next was built idle, so Finish is a no-op for it; idle the generation the
	// runtime actually observes or the runtime stays busy and cannot close.
	runtime.NoteExecution(next.gen, RuntimeIdle, "")
}

func TestReplaceExecutionRejectsBusyOwner(t *testing.T) {
	_, runtime := reviewRuntime(t)
	old := &testExecution{phase: RuntimeRunning, runtime: runtime}
	old.gen = runtime.BindExecution(old)
	runtime.NoteExecution(old.gen, RuntimeRunning, "old")
	if gen := runtime.ReplaceExecution(old.gen, &testExecution{}); gen != 0 {
		t.Fatalf("busy replacement generation = %d, want rejection", gen)
	}
	if got := runtime.StateSnapshot().Phase; got != RuntimeRunning {
		t.Fatalf("phase after rejected replacement = %s, want running", got)
	}
	old.Finish()
}

func TestStaleExecutionGenerationCannotCommitPreparedBatch(t *testing.T) {
	_, runtime := reviewRuntime(t)
	old := &testExecution{phase: RuntimeIdle, runtime: runtime}
	old.gen = runtime.BindExecution(old)
	prepared, err := runtime.Session().PrepareBatchContext(t.Context(), "old-config", Batch{
		Events: []Event{{Kind: "session/config", Payload: []byte(`{"modelRef":"old"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	next := &testExecution{phase: RuntimeIdle, runtime: runtime}
	next.gen = runtime.ReplaceExecution(old.gen, next)
	if next.gen == 0 {
		t.Fatal("replace idle execution")
	}
	if _, err := runtime.CommitPreparedForExecution(old.gen, prepared); !errors.Is(err, ErrStaleExecution) {
		t.Fatalf("stale commit error = %v, want %v", err, ErrStaleExecution)
	}
	if got := runtime.Session().ExecutionSnapshot().EventSequence; got != 0 {
		t.Fatalf("stale generation committed sequence %d", got)
	}
	current, err := runtime.Session().PrepareBatchContext(t.Context(), "new-config", Batch{
		Events: []Event{{Kind: "session/config", Payload: []byte(`{"modelRef":"new"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.CommitPreparedForExecution(next.gen, current); err != nil {
		t.Fatalf("current generation commit: %v", err)
	}
}

func TestCancelRetriesAcrossExecutionCutover(t *testing.T) {
	_, runtime := reviewRuntime(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	old := &blockingRejectExecution{entered: entered, release: release}
	oldGeneration := runtime.BindExecution(old)
	if oldGeneration == 0 {
		t.Fatal("bind outgoing execution")
	}
	result := make(chan bool, 1)
	go func() { result <- runtime.Cancel() }()
	<-entered
	next := &testExecution{phase: RuntimeRunning, runtime: runtime}
	next.gen = runtime.ReplaceExecution(oldGeneration, next)
	if next.gen == 0 {
		t.Fatal("replace execution while cancel is in flight")
	}
	close(release)
	if accepted := <-result; !accepted {
		t.Fatal("cancel was lost across execution cutover")
	}
	if got := next.phase; got != RuntimeCancelling {
		t.Fatalf("replacement phase = %s, want cancelling", got)
	}
}

func TestRuntimeFinalizingIsBusy(t *testing.T) {
	if !RuntimeFinalizing.Busy() {
		t.Fatal("finalizing phase must remain busy until terminal commit finishes")
	}
}
