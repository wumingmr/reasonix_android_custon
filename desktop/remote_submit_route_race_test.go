package main

import (
	"context"
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

// A submit that races a session switch is refused by the serve's
// expected-session fence ("active session changed"). The desktop must retry it
// against the settled route instead of surfacing a failure the user cannot act
// on, and must report a transient outcome once the retry window closes.
func TestRemoteSubmitRacingRouteChangeRetriesThenReportsTransient(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "remote.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := remoteInboxRunner{started: make(chan string, 4), release: make(chan struct{}, 4)}
	sink := &remoteInboxSink{states: make(chan event.RuntimeStateSnapshot, 64)}
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Runner: runner, Sink: sink})
	server := httptest.NewServer(operatorServeHandler(serve.New(ctrl, nil, config.ServeConfig{})))
	defer server.Close()
	t.Cleanup(func() { closeRemoteTestController(t, ctrl) })

	a, tab := remoteRuntimeTestApp(server.Client())
	a.ctx = context.Background()
	tab.base, tab.session.path = server.URL, path

	// The desktop's route is ahead of the serve's foreground (the shape of a
	// submit that raced a switch): every attempt is fenced.
	tab.routing.currentPath = "session-id:not-the-current-session"
	err := a.SubmitRemoteTabWithSubmission(tab.id, "raced message", "route-race-1")
	var coded interface {
		error
		RPCErrorData() map[string]any
	}
	if !errors.As(err, &coded) || coded.Error() != "reasonix_error:inbox_target_transient" {
		t.Fatalf("raced submit = %v, want the transient outcome", err)
	}
	if coded.RPCErrorData()["transient"] != true {
		t.Fatalf("transient outcome carries no transient marker: %v", coded.RPCErrorData())
	}

	// Once the route settles the same message submits normally.
	tab.routing.currentPath = path
	if err := a.SubmitRemoteTabWithSubmission(tab.id, "settled message", "route-race-2"); err != nil {
		t.Fatalf("settled submit failed: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the settled submit never started a turn")
	}

	// Let the previous turn finish so the next case tests the route fence and
	// not the busy fence.
	runner.release <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for ctrl.Running() {
		if time.Now().After(deadline) {
			t.Fatal("turn did not finish before the route-recovery case")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A route that settles inside the retry window is recovered silently.
	tab.routing.currentPath = "session-id:not-the-current-session"
	go func() {
		time.Sleep(60 * time.Millisecond)
		a.remoteTabMu.Lock()
		if current := a.remoteTabs[tab.id]; current != nil {
			current.routing.currentPath = path
		}
		a.remoteTabMu.Unlock()
	}()
	if err := a.SubmitRemoteTabWithSubmission(tab.id, "recovered message", "route-race-3"); err != nil {
		t.Fatalf("submit recovered by a settling route failed: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the recovered submit never started a turn")
	}
	runner.release <- struct{}{}
}
