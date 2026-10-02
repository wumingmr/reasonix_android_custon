package control

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

type blockingMaintenanceProvider struct {
	started   chan struct{}
	cancelled chan struct{}
	once      sync.Once
}

type failNthMaintenanceEventSink struct {
	mu      sync.Mutex
	n       int
	failAt  int
	failure error
}

func (s *failNthMaintenanceEventSink) Emit(event.Event) {}
func (s *failNthMaintenanceEventSink) EmitChecked(e event.Event) error {
	if e.Kind != event.SessionOperation {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	if s.n == s.failAt {
		return s.failure
	}
	return nil
}

func (p *blockingMaintenanceProvider) Name() string { return "blocking-maintenance" }
func (p *blockingMaintenanceProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	close(p.cancelled)
	return nil, ctx.Err()
}

func maintenanceFixtureSession() *agent.Session {
	sess := agent.NewSession("sys")
	for range 8 {
		sess.Add(provider.Message{Role: provider.RoleUser, Content: strings.Repeat("question ", 300)})
		sess.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("answer ", 300)})
	}
	return sess
}

func TestManualCompactIsCancellableForegroundMaintenance(t *testing.T) {
	prov := &blockingMaintenanceProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	exec := agent.New(prov, nil, maintenanceFixtureSession(), agent.Options{ContextWindow: 32_000}, event.Discard)
	// Cancellation and foreground ownership do not require filesystem writes.
	// TestMaintenanceLifecycleDurableOutsideTurn covers the durable boundary.
	c := newOwnedTestController(t, Options{Executor: exec, SystemPrompt: "sys", Sink: event.Discard})

	done := make(chan error, 1)
	go func() { done <- c.Compact(context.Background(), "") }()
	select {
	case <-prov.started:
	case <-time.After(3 * time.Second):
		t.Fatal("summary provider was not called")
	}

	snapshot := c.RuntimeStateSnapshot()
	if !snapshot.Running || !snapshot.Cancellable || snapshot.Maintenance == nil || snapshot.Maintenance.Kind != "compact" {
		t.Fatalf("runtime during compact = %+v", snapshot)
	}
	receipt := c.CancelSessionFrom("test")
	if receipt.AlreadyIdle {
		t.Fatalf("CancelSession reported idle during compaction: %+v", receipt)
	}
	select {
	case <-prov.cancelled:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel the summary request")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Compact error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("compaction did not finish after cancellation")
	}

	if snapshot = c.RuntimeStateSnapshot(); snapshot.Running || snapshot.Maintenance != nil {
		t.Fatalf("runtime after cancelled compact = %+v", snapshot)
	}
}

func TestCompactSubmitReceiptIncludesRegisteredOperation(t *testing.T) {
	prov := &blockingMaintenanceProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	exec := agent.New(prov, nil, maintenanceFixtureSession(), agent.Options{ContextWindow: 32_000}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard})
	result := c.SubmitDisplayWithResult("/compact", "/compact")
	if result.Disposition != SubmitManagementHandled || result.OperationID == "" {
		t.Fatalf("compact receipt = %+v", result)
	}
	if got := c.ActiveMaintenanceOperationID(); got != result.OperationID {
		t.Fatalf("active operation = %q, receipt = %q", got, result.OperationID)
	}
	select {
	case <-prov.started:
	case <-time.After(3 * time.Second):
		t.Fatal("summary provider was not called")
	}
	c.CancelSessionFrom("test")
	select {
	case <-prov.cancelled:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel the submitted compaction")
	}
}

func TestMaintenanceBlocksRotationAndInboxDispatch(t *testing.T) {
	c := newOwnedTestController(t, Options{Executor: agent.New(nil, nil, maintenanceFixtureSession(), agent.Options{}, event.Discard), Sink: event.Discard})
	op, _, err := c.beginMaintenance(context.Background(), "compact")
	if err != nil {
		t.Fatalf("beginMaintenance: %v", err)
	}
	if err := c.beginRotation(); !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("beginRotation during maintenance = %v, want ErrMaintenanceBusy", err)
	}
	if _, _, err := c.beginMaintenance(context.Background(), "compact"); !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("second maintenance = %v, want ErrMaintenanceBusy", err)
	}
	c.mu.Lock()
	busy := c.maintenance == op
	c.mu.Unlock()
	if !busy || !c.Running() {
		t.Fatal("maintenance was not retained as foreground work")
	}
	_, _, _ = c.signalMaintenanceCancel()
	c.mu.Lock()
	c.maintenance = nil
	close(op.done)
	c.mu.Unlock()
}

func TestMaintenanceStopNeverWithdrawsQueuedItemsDuringFinalizing(t *testing.T) {
	c := newOwnedTestController(t, Options{Sink: event.Discard})
	op, _, err := c.beginMaintenance(context.Background(), "compact")
	if err != nil {
		t.Fatalf("beginMaintenance: %v", err)
	}
	c.mu.Lock()
	op.activity = "finalizing"
	c.mu.Unlock()
	result, err := c.CancelWithInboxItemsResult([]string{"queued-1"}, "test")
	if err != nil {
		t.Fatalf("CancelWithInboxItemsResult: %v", err)
	}
	if len(result.DiscardedItemIDs) != 0 {
		t.Fatalf("discarded queued items during finalizing: %v", result.DiscardedItemIDs)
	}
	c.mu.Lock()
	c.maintenance = nil
	close(op.done)
	c.mu.Unlock()
}

func TestCloseWaitsForMaintenanceThatIgnoredCancellation(t *testing.T) {
	c := newOwnedTestController(t, Options{Sink: event.Discard})
	c.testCancelGrace = 5 * time.Millisecond
	op, workCtx, err := c.beginMaintenance(context.Background(), "compact")
	if err != nil {
		t.Fatalf("beginMaintenance: %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- c.executeMaintenance(op, workCtx, func(context.Context) error {
			close(started)
			<-release // deliberately ignore cancellation until the owner releases us
			return workCtx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance worker did not start")
	}
	c.CancelSessionFrom("test")
	deadline := time.Now().Add(time.Second)
	recoveryObserved := false
	for time.Now().Before(deadline) {
		snapshot := c.RuntimeStateSnapshot()
		if snapshot.Maintenance != nil && snapshot.Maintenance.Activity == "recovery_required" {
			recoveryObserved = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !recoveryObserved {
		t.Fatal("maintenance did not enter recovery after ignoring cancellation")
	}
	c.Close()
	select {
	case <-c.Closed():
		t.Fatal("controller resources closed while the maintenance worker was still running")
	default:
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Compact error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("late maintenance worker did not settle")
	}
	select {
	case <-c.Closed():
	case <-time.After(3 * time.Second):
		t.Fatal("controller did not release resources after maintenance settled")
	}
}

func TestTerminalOperationPersistenceFailureRetainsRecovery(t *testing.T) {
	want := errors.New("operation journal unavailable")
	sink := &failNthMaintenanceEventSink{failAt: 2, failure: want}
	c := newOwnedTestController(t, Options{Sink: event.Discard})
	op, workCtx, err := c.beginMaintenance(context.Background(), "compact")
	if err != nil {
		t.Fatalf("beginMaintenance: %v", err)
	}
	c.sink = sink // finalizing succeeds; the terminal record fails
	err = c.executeMaintenance(op, workCtx, func(context.Context) error { return nil })
	if !errors.Is(err, want) {
		t.Fatalf("executeMaintenance error = %v, want %v", err, want)
	}
	snapshot := c.RuntimeStateSnapshot()
	if snapshot.Maintenance == nil || snapshot.Maintenance.Activity != "recovery_required" || !snapshot.Running || snapshot.Cancellable {
		t.Fatalf("runtime after terminal persistence failure = %+v", snapshot)
	}
}

func TestMaintenanceDoesNotStartWhenOperationRecordCannotPersist(t *testing.T) {
	want := errors.New("operation journal unavailable")
	c := newOwnedTestController(t, Options{Sink: event.Discard})
	c.sink = &failNthMaintenanceEventSink{failAt: 1, failure: want}
	if _, _, err := c.beginMaintenance(context.Background(), "compact"); !errors.Is(err, want) {
		t.Fatalf("beginMaintenance error = %v, want %v", err, want)
	}
	if state := c.RuntimeStateSnapshot(); state.Maintenance == nil || state.Maintenance.Status != "recovery_required" || state.Cancellable {
		t.Fatalf("failed operation registration must retain a recovery barrier: %+v", state)
	}
}

func TestEmptyManualCompactIsNoop(t *testing.T) {
	c := newOwnedTestController(t, Options{
		Executor: agent.New(nil, nil, agent.NewSession("sys"), agent.Options{ContextWindow: 32_000}, event.Discard),
		Sink:     event.Discard,
	})
	var terminal string
	c.sink = event.FuncSink(func(e event.Event) {
		if e.SessionOperation != nil {
			terminal = e.SessionOperation.Status
		}
	})
	if err := c.Compact(context.Background(), ""); err != nil {
		t.Fatalf("Compact(empty) = %v, want nil", err)
	}
	if terminal != "noop" {
		t.Fatalf("terminal status = %q, want noop", terminal)
	}
}

func TestMaintenanceCompletionDrainsParkedGuidance(t *testing.T) {
	c := newOwnedTestController(t, Options{Sink: event.Discard})
	op, ctx, err := c.beginMaintenance(context.Background(), "compact")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.submitSteerFallback("retain this guidance"); got != turnParked {
		t.Fatalf("admission = %v, want turnParked", got)
	}
	if err := c.executeMaintenance(op, ctx, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	pending := len(c.turns.pending)
	active := c.bodyActiveLocked()
	c.mu.Unlock()
	if pending != 0 && !active {
		t.Fatalf("maintenance ended idle with %d stranded guidance item(s)", pending)
	}
}

func TestSynchronousTurnCannotEnterMaintenance(t *testing.T) {
	c := newOwnedTestController(t, Options{Sink: event.Discard})
	op, ctx, err := c.beginMaintenance(context.Background(), "compact")
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	err = c.runSynchronousTurn(context.Background(), nil, func(context.Context) error {
		ran = true
		return nil
	})
	if ran || !errors.Is(err, ErrMaintenanceBusy) || !errors.Is(err, ErrTurnRunning) {
		t.Fatalf("synchronous turn during maintenance: ran=%v err=%v", ran, err)
	}
	if err := c.executeMaintenance(op, ctx, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestRunInboxTurnRemainsQueuedDuringMaintenance(t *testing.T) {
	dir := t.TempDir()
	c := newOwnedTestController(t, Options{
		SessionDir: dir, SessionPath: filepath.Join(dir, "session.jsonl"), Sink: event.Discard,
	})
	op, ctx, err := c.beginMaintenance(context.Background(), "compact")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := c.EnqueueInbox(InboxRequest{Submit: "queued during compact", Idempotency: "maintenance-inbox"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RunInboxTurn(context.Background(), receipt.ItemID); !errors.Is(err, ErrMaintenanceBusy) || !errors.Is(err, ErrTurnRunning) {
		t.Fatalf("RunInboxTurn error = %v, want retryable maintenance busy", err)
	}
	meta, _, err := c.ReadInboxItem(receipt.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != "queued" {
		t.Fatalf("inbox state = %q, want queued", meta.State)
	}
	if err := c.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	if err := c.executeMaintenance(op, ctx, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalMaintenanceOperationRejectsLateCancellingEvent(t *testing.T) {
	c := newOwnedTestController(t, Options{Sink: event.Discard})
	op, ctx, err := c.beginMaintenance(context.Background(), "compact")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	stopped := make(chan struct{})
	var mu sync.Mutex
	var statuses []string
	c.sink = event.FuncSink(func(e event.Event) {
		if e.SessionOperation == nil {
			return
		}
		status := e.SessionOperation.Status
		if status == "cancelling" {
			close(entered)
			<-release
		}
		mu.Lock()
		statuses = append(statuses, status)
		mu.Unlock()
	})
	go func() {
		c.signalMaintenanceCancel()
		close(stopped)
	}()
	<-entered
	finished := make(chan error, 1)
	go func() {
		finished <- c.executeMaintenance(op, ctx, func(context.Context) error { return ctx.Err() })
	}()
	close(release)
	<-stopped
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("executeMaintenance = %v, want context.Canceled", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(statuses) == 0 || statuses[len(statuses)-1] != "cancelled" {
		t.Fatalf("operation statuses = %v, want terminal cancelled last", statuses)
	}
}

func TestMaintenanceOperationRevisionsAreMonotonicAndSnapshotIsLossless(t *testing.T) {
	var mu sync.Mutex
	var records []event.SessionOperationInfo
	sink := event.FuncSink(func(e event.Event) {
		if e.SessionOperation == nil {
			return
		}
		mu.Lock()
		records = append(records, *e.SessionOperation)
		mu.Unlock()
	})
	c := newOwnedTestController(t, Options{Sink: sink})
	op, ctx, err := c.beginMaintenance(context.Background(), "compact")
	if err != nil {
		t.Fatal(err)
	}
	running := c.RuntimeStateSnapshot().Maintenance
	if running == nil || running.OperationID != op.id || running.OperationRevision == 0 || running.Status != "running" {
		t.Fatalf("running maintenance snapshot = %+v", running)
	}
	if err := c.executeMaintenance(op, ctx, func(context.Context) error { return errors.New("summary unavailable") }); err == nil {
		t.Fatal("executeMaintenance unexpectedly succeeded")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(records) != 3 {
		t.Fatalf("operation records = %+v, want running/finalizing/failed", records)
	}
	for i, record := range records {
		if record.OperationRevision != uint64(i+1) {
			t.Fatalf("record %d revision = %d, want %d", i, record.OperationRevision, i+1)
		}
		if record.RuntimeEpoch != records[0].RuntimeEpoch {
			t.Fatalf("record %d runtime epoch = %q, want %q", i, record.RuntimeEpoch, records[0].RuntimeEpoch)
		}
	}
	if records[2].Status != "failed" || records[2].ErrorCode != "summary_failed" || records[2].Detail == "" {
		t.Fatalf("terminal record = %+v", records[2])
	}
}
