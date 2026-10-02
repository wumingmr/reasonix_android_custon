package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/serve"
)

// A foreground submit that lands while the previous turn is still running must
// fall back to the durable inbox follow-up the serve asks for, instead of
// surfacing "session is busy" to the user.
func TestRemoteBusySubmitFallsBackToDurableInbox(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "remote.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := remoteInboxRunner{started: make(chan string, 4), release: make(chan struct{}, 4)}
	sink := &remoteInboxSink{states: make(chan event.RuntimeStateSnapshot, 64)}
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Runner: runner, Sink: sink})
	t.Cleanup(func() {
		runner.release <- struct{}{}
		runner.release <- struct{}{}
		closeRemoteTestController(t, ctrl)
	})
	server := httptest.NewServer(operatorServeHandler(serve.New(ctrl, nil, config.ServeConfig{})))
	defer server.Close()
	a, tab := remoteRuntimeTestApp(server.Client())
	tab.base, tab.routing.currentPath, tab.session.path = server.URL, path, path
	// emitInboxChanged requires a context; production App always has one.
	a.ctx = context.Background()

	// Turn 1 occupies the foreground.
	ctrl.Send("hold the foreground")
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first turn did not start")
	}

	// A submit during the running turn must queue durably and report the receipt
	// as a queued outcome, so the renderer can show the visible queue entry
	// immediately instead of a failure.
	events := make([]string, 0, 4)
	previousEmit := runtimeEventsEmitFallback
	runtimeEventsEmitFallback = func(_ context.Context, name string, payload ...any) {
		if name == "InboxChanged" {
			events = append(events, name)
		}
	}
	t.Cleanup(func() { runtimeEventsEmitFallback = previousEmit })

	err := a.SubmitRemoteTabWithSubmission(tab.id, "follow-up message", "submission-busy-1")
	var queued interface{ RPCErrorData() map[string]any }
	if !errors.As(err, &queued) {
		t.Fatalf("busy submit = %v, want a queuedFollowupError", err)
	}
	data := queued.RPCErrorData()
	var receipt InboxReceiptView
	raw, _ := json.Marshal(data["queuedFollowup"])
	if json.Unmarshal(raw, &receipt) != nil || receipt.ItemID == "" || receipt.Disposition == "" {
		t.Fatalf("queued outcome carries no valid receipt: %v", data)
	}
	if len(events) != 1 {
		t.Fatalf("remote enqueue emitted %d InboxChanged events, want 1", len(events))
	}
	snapshot, err := a.InboxSnapshot(tab.id)
	if err != nil || len(snapshot.Items) != 1 || snapshot.Items[0].Preview != "follow-up message" || snapshot.Items[0].ID != receipt.ItemID {
		t.Fatalf("queued receipt did not resolve to a durable item: %+v, %v", snapshot, err)
	}

	// The idempotency key rides the submission id: a retried identical submit
	// must not double-queue.
	if err := a.SubmitRemoteTabWithSubmission(tab.id, "follow-up message", "submission-busy-1"); err == nil {
		t.Fatal("retried busy submit reported a started turn")
	} else if !errors.As(err, &queued) {
		t.Fatalf("retried busy submit = %v, want a queued outcome", err)
	}
	snapshot, err = a.InboxSnapshot(tab.id)
	if err != nil || len(snapshot.Items) != 1 {
		t.Fatalf("retried submit double-queued: %+v, %v", snapshot, err)
	}

	// Release turn 1; the queued follow-up dispatches as turn 2.
	runner.release <- struct{}{}
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("queued follow-up was not dispatched")
	}
	runner.release <- struct{}{}
}
