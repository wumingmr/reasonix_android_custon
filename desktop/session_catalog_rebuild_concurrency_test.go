package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/sessioncatalog"
)

func TestConcurrentRebuildSessionCatalogCallersShareFailure(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.startSessionCatalog()
	_ = waitForSessionCatalogForTest(t, app, nil)
	t.Cleanup(func() { app.stopSessionCatalog(time.Second) })

	reconcileStarted := make(chan struct{})
	reconcileRelease := make(chan struct{})
	var reconcileDone []<-chan struct{}
	t.Cleanup(func() {
		close(reconcileRelease)
		for _, done := range reconcileDone {
			if !waitChannelBefore(done, time.Now().Add(5*time.Second)) {
				t.Error("reconcile did not exit after release")
			}
		}
	})
	app.catalogReconcileHook = func(sessioncatalog.DirectoryTarget) {
		close(reconcileStarted)
		<-reconcileRelease
	}
	if !app.requestSessionCatalogReconcile(dir) {
		t.Fatal("explicit reconcile was not scheduled")
	}
	<-reconcileStarted
	app.catalogReconcileMu.Lock()
	for _, job := range app.catalogReconcileJobs {
		reconcileDone = append(reconcileDone, job.done)
	}
	app.catalogReconcileMu.Unlock()

	app.ctx = context.Background()
	rebuildStarted := make(chan struct{})
	joined := make(chan struct{})
	rebuildRelease := make(chan struct{}, 1)
	defer close(rebuildRelease)
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...any) {
		if name != "project-tree:changed-v2" || len(payload) != 1 {
			return
		}
		event, ok := payload[0].(ProjectTreeChangedV2)
		if ok && event.Reason == "catalog_rebuild_started" {
			close(rebuildStarted)
			// Keep the flight published until the follower has joined. Hold
			// reconcile blocked through both results to force the exact stop
			// failure independently of SQLite/disk speed.
			<-rebuildRelease
		}
	}
	app.catalogRebuildJoinHook = func() { close(joined) }

	leaderDone := make(chan error, 1)
	go func() { leaderDone <- app.rebuildSessionCatalog(25 * time.Millisecond) }()
	<-rebuildStarted
	followerDone := make(chan error, 1)
	go func() { followerDone <- app.rebuildSessionCatalog(25 * time.Millisecond) }()
	<-joined
	select {
	case err := <-followerDone:
		t.Fatalf("concurrent rebuild returned before the owner completed: %v", err)
	default:
	}

	rebuildRelease <- struct{}{}
	leaderErr := <-leaderDone
	followerErr := <-followerDone
	if leaderErr == nil || followerErr == nil {
		t.Fatalf("concurrent rebuild errors = leader %v, follower %v; want shared failure", leaderErr, followerErr)
	}
	if !errors.Is(followerErr, leaderErr) || !errors.Is(leaderErr, errSessionCatalogStopTimeout) {
		t.Fatalf("concurrent rebuild errors = leader %q, follower %q; want the same rebuild result",
			leaderErr, followerErr)
	}
	if app.catalogRebuilding.Load() {
		t.Fatal("shared failed rebuild left rebuilding set")
	}
}
