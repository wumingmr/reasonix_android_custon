package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/extension"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestMaintenanceRegressionMaintenanceRuntimeOwnership(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "maintenance-reaudit"})
	if err != nil {
		t.Fatal(err)
	}
	newController := func() *Controller {
		exec := agent.New(nil, nil, agent.NewSession("system"), agent.Options{}, event.Discard)
		return newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	}
	first := newController()
	second := newController()
	before := runtime.Session().StateSnapshot().EventSequence
	op, ctx, err := first.beginMaintenance(context.Background(), "compact")
	if err != nil {
		t.Fatal(err)
	}
	defer first.finishMaintenanceOperation(op)
	if after := runtime.Session().StateSnapshot().EventSequence; after == before {
		t.Errorf("maintenance start was never appended to session log: sequence stayed %d", after)
	}
	if got := runtime.StateSnapshot().Phase; got != session.RuntimeRunning {
		t.Errorf("maintenance running, shared runtime phase = %s, want running", got)
	}
	if _, _, err := second.beginMaintenance(t.Context(), "compact"); !errors.Is(err, ErrMaintenanceBusy) {
		t.Errorf("unpublished controller maintenance admission = %v", err)
	}
	if runtime.Cancel(); ctx.Err() == nil {
		t.Error("shared runtime Cancel did not cancel maintenance context")
	}
	if err := ActivateControllerReplacement(first, second); err == nil {
		t.Error("replacement stole execution ownership while maintenance was active")
	}
}

func TestMaintenanceLifecycleDurableOutsideTurn(t *testing.T) {
	for _, status := range []string{"noop", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			persistence := session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions"))
			service, err := session.NewService("desktop", persistence)
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "durable-maintenance"})
			if err != nil {
				t.Fatal(err)
			}
			exec := agent.New(nil, nil, agent.NewSession("system"), agent.Options{}, event.Discard)
			c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
			before := runtime.Session().ExecutionSnapshot().Projection
			op, ctx, err := c.beginMaintenance(t.Context(), "compact")
			if err != nil {
				t.Fatal(err)
			}
			if state := runtime.Session().StateSnapshot(); state.DurableSequence != state.EventSequence {
				t.Fatal("start returned before durable checkpoint")
			}
			err = c.executeMaintenance(op, ctx, func(context.Context) error {
				switch status {
				case "failed":
					return errors.New("summary test failure")
				case "cancelled":
					return context.Canceled
				}
				return nil
			})
			if status == "noop" && err != nil {
				t.Fatal(err)
			}
			after := runtime.Session().StateSnapshot()
			if after.DurableSequence != after.EventSequence {
				t.Fatal("maintenance released before durable terminal")
			}
			projection := runtime.Session().ExecutionSnapshot().Projection
			if !reflect.DeepEqual(before.Messages, projection.Messages) || !reflect.DeepEqual(before.ModelMessages, projection.ModelMessages) || len(projection.Turns) != len(before.Turns) {
				t.Fatal("display operation changed canonical/model history or turn count")
			}
			if got := runtime.StateSnapshot().Phase; got != session.RuntimeIdle {
				t.Fatalf("settled runtime = %s", got)
			}
			c.Close()
			<-c.Closed()
			if err := service.CloseAll(t.Context()); err != nil {
				t.Fatal(err)
			}
			cold, err := persistence.Open(runtime.Ref().SessionID, session.ReadOnly)
			if err != nil {
				t.Fatal(err)
			}
			defer cold.Close(context.Background())
			page, err := cold.Read(t.Context(), 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			var records []event.SessionOperationInfo
			for _, commit := range page.Commits {
				for _, e := range commit.Events {
					if e.Kind != "diagnostic" {
						continue
					}
					var payload struct {
						Type   string           `json:"type"`
						Record provider.Message `json:"displayRecord"`
					}
					if err := json.Unmarshal(e.Payload, &payload); err != nil {
						t.Fatal(err)
					}
					if payload.Type != "session-maintenance-v1" {
						continue
					}
					if !e.Optional {
						t.Fatal("maintenance record must be ignorable by older readers")
					}
					var info event.SessionOperationInfo
					if err := json.Unmarshal([]byte(payload.Record.Content), &info); err != nil {
						t.Fatal(err)
					}
					records = append(records, info)
				}
			}
			if len(records) != 3 || records[0].Status != "running" || records[2].Status != status || records[2].OperationRevision != 3 {
				t.Fatalf("reopened records = %+v", records)
			}
			if status == "failed" && records[2].Detail != "summary test failure" {
				t.Fatal("failure details lost on reopen")
			}
			// The actual canonical history window collapses revisions into one row.
			reader, err := session.NewService("desktop", persistence)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.CloseAll(context.Background())
			deadline := time.Now().Add(5 * time.Second)
			for {
				window, err := reader.Query().ReadHistoryWindow(t.Context(), runtime.Ref(), session.HistoryWindowRequest{Anchor: "newest", Limit: 10})
				if err != nil {
					t.Fatal(err)
				}
				if window.Status == "preparing" && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
					continue
				}
				if window.Status != "ready" {
					t.Fatalf("history status = %s", window.Status)
				}
				var count int
				for _, row := range window.Messages {
					if row.MessageID == "maintenance:"+op.id {
						count++
						var message provider.Message
						if err := json.Unmarshal(row.Inline, &message); err != nil {
							t.Fatal(err)
						}
						var info event.SessionOperationInfo
						if err := json.Unmarshal([]byte(message.Content), &info); err != nil {
							t.Fatal(err)
						}
						if info.Status != status || info.OperationRevision != 3 {
							t.Fatalf("history terminal = %+v", info)
						}
					}
				}
				if count != 1 {
					t.Fatalf("maintenance history rows = %d", count)
				}
				break
			}
		})
	}
}

func TestMaintenanceAdmissionRejectsUnpublishedAndDrainingOwners(t *testing.T) {
	owner := extension.NewRuntimeOwner()
	owner.Gate.Publish(2)
	c := newOwnedTestController(t, Options{RuntimeOwner: owner, RuntimeGeneration: 1, Sink: event.Discard})
	if op, _, err := c.beginMaintenance(t.Context(), "compact"); !errors.Is(err, ErrRuntimeDraining) {
		if op != nil {
			c.finishMaintenanceOperation(op)
		}
		t.Fatalf("draining admission = %v", err)
	}
}

func TestMaintenanceTimeoutSettlesSharedRuntimeAndDrainsQueue(t *testing.T) {
	service, err := session.NewService("desktop", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "timeout"})
	if err != nil {
		t.Fatal(err)
	}
	recovering := make(chan struct{}, 1)
	sink := event.FuncSink(func(e event.Event) {
		if e.SessionOperation != nil && e.SessionOperation.Status == "recovery_required" {
			recovering <- struct{}{}
		}
	})
	exec := agent.New(nil, nil, agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: sink, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
	c.testCancelGrace = time.Millisecond
	op, ctx, err := c.beginMaintenance(t.Context(), "compact")
	if err != nil {
		t.Fatal(err)
	}
	defer c.finishMaintenanceOperation(op)
	queued := make(chan struct{}, 1)
	if got := c.runGuardedOrPark(func(context.Context) error { queued <- struct{}{}; return nil }); got != turnParked {
		t.Fatalf("queued admission = %v", got)
	}
	if !runtime.Cancel() {
		t.Fatal("runtime did not acknowledge Stop")
	}
	select {
	case <-recovering:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout recovery was not published")
	}
	if runtime.StateSnapshot().Phase != session.RuntimeRecoveryRequired {
		t.Fatal("runtime not fenced during timeout")
	}
	select {
	case <-queued:
		t.Fatal("queue ran during recovery")
	default:
	}
	// The ignored worker has now exited with cancellation and no commit. The
	// durable terminal is sufficient to release timeout recovery, not a model
	// turn or controller replacement that could consume the queued request.
	if err := c.executeMaintenance(op, ctx, func(context.Context) error { return ctx.Err() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("settled worker = %v", err)
	}
	select {
	case <-queued:
	case <-time.After(3 * time.Second):
		t.Fatal("queue did not resume after safe settlement")
	}
	waitIdleAdmission(t, c)
	if runtime.StateSnapshot().Phase != session.RuntimeIdle {
		t.Fatal("shared runtime remained busy after queue drained")
	}
}

func TestMaintenanceDurabilityFailureRetainsOwner(t *testing.T) {
	for _, boundary := range []string{"start", "terminal"} {
		t.Run(boundary, func(t *testing.T) {
			var fail atomic.Bool
			store, err := session.CreateWithOptions(filepath.Join(t.TempDir(), "store"), "failure", session.OpenOptions{Sync: func(f *os.File) error {
				if fail.Load() {
					return errors.New("injected maintenance sync failure")
				}
				return f.Sync()
			}})
			if err != nil {
				t.Fatal(err)
			}
			service, err := session.NewService("desktop", failingFlushPersistence{session: store})
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "failure"})
			if err != nil {
				t.Fatal(err)
			}
			var completed atomic.Bool
			sink := event.FuncSink(func(e event.Event) {
				if e.SessionOperation != nil {
					if e.SessionOperation.Status == "finalizing" && boundary == "terminal" {
						fail.Store(true)
					}
					if maintenanceTerminalStatus(e.SessionOperation.Status) {
						completed.Store(true)
					}
				}
			})
			exec := agent.New(nil, nil, agent.NewSession("system"), agent.Options{}, event.Discard)
			c := newOwnedTestController(t, Options{Executor: exec, Sink: sink, SessionService: service, SessionRuntime: runtime, ExclusiveSession: true})
			if boundary == "start" {
				fail.Store(true)
			}
			op, ctx, err := c.beginMaintenance(t.Context(), "compact")
			if boundary == "terminal" {
				if err != nil {
					t.Fatal(err)
				}
				err = c.executeMaintenance(op, ctx, func(context.Context) error { return nil })
			}
			if err == nil {
				t.Fatal("failed sync reported success")
			}
			state := c.RuntimeStateSnapshot()
			if state.Maintenance == nil || state.Maintenance.Status != "recovery_required" || state.Cancellable || completed.Load() {
				t.Fatalf("persistence failure escaped recovery: %+v completed=%v", state, completed.Load())
			}
			if runtime.StateSnapshot().Phase != session.RuntimeRecoveryRequired {
				t.Fatal("shared runtime lost recovery barrier")
			}
		})
	}
}

func TestMaintenanceRegressionEmptyPositionalCompression(t *testing.T) {
	sess := agent.NewSession("system")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "first user message"})
	exec := agent.New(nil, nil, sess, agent.Options{ContextWindow: 32000}, event.Discard)
	if err := exec.SummarizeUpTo(t.Context(), 1); err != nil {
		t.Errorf("empty range before first user should be noop, got %v", err)
	}
}

func TestMaintenanceRegressionLateCancelCannotReopenFinalizing(t *testing.T) {
	c := newOwnedTestController(t, Options{Sink: event.Discard})
	op, _, err := c.beginMaintenance(context.Background(), "compact")
	if err != nil {
		t.Fatal(err)
	}
	defer c.finishMaintenanceOperation(op)
	// Stop captures running, signals the worker, then is descheduled before
	// publishing. The worker reaches finalization in that exact interval.
	entered, resume, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cancel := op.cancel
	op.cancel = func() { cancel(); close(entered); <-resume }
	go func() { c.CancelSession(); close(returned) }()
	<-entered
	snapshot := c.ContextMaintenanceSnapshot()
	if err := c.emitMaintenanceOperation(op, "finalizing", "", "", snapshot, false); err != nil {
		t.Fatal(err)
	}
	close(resume)
	<-returned
	op.cancel = cancel
	if got := c.RuntimeStateSnapshot(); got.Maintenance.Activity != "finalizing" || got.Cancellable {
		t.Errorf("late cancellation reopened finalization: activity=%s cancellable=%v", got.Maintenance.Activity, got.Cancellable)
	}
}
