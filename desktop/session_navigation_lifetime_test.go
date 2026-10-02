package main

import (
	"context"
	"errors"
	"testing"
)

func TestSessionOpenKeepsPublishedControllerContextAlive(t *testing.T) {
	app, _, target, _, _ := canonicalWorkspaceOpenFixture(t)
	appCtx, cancelApp := context.WithCancel(app.ctx)
	t.Cleanup(cancelApp)
	app.ctx = appCtx
	var buildCtx context.Context
	app.sessionOpenBuildHook = func(ctx context.Context) { buildCtx = ctx }
	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatal(err)
	}
	if buildCtx == nil || buildCtx.Err() != nil {
		t.Fatalf("successful open cancelled the controller lifetime: %v", buildCtx)
	}
	app.cancelSessionNavigation()
	if err := buildCtx.Err(); err != nil {
		t.Fatalf("later navigation cancellation stopped the published controller: %v", err)
	}
	cancelApp()
	if !errors.Is(buildCtx.Err(), context.Canceled) {
		t.Fatal("published controller lost the application lifetime")
	}
}

func TestSessionOpenRejectsAdmissionAfterShutdownCancellation(t *testing.T) {
	app, tab, target, _, _ := canonicalWorkspaceOpenFixture(t)
	previous := tab.Ctrl
	app.shuttingDown.Store(true)
	app.cancelSessionNavigation()
	if _, err := app.OpenSession(target.Ref()); err == nil {
		t.Fatal("session open was admitted after shutdown cancellation")
	}
	if tab.Ctrl != previous {
		t.Fatal("late navigation replaced the shutdown controller")
	}
}

func TestStaleSessionResumeDoesNotCancelCurrentNavigation(t *testing.T) {
	app, tab, target, _, _ := canonicalWorkspaceOpenFixture(t)
	stale := app.desktopSessions.navigationSeq.Add(1)
	app.desktopSessions.navigationSeq.Add(1)
	ctx, finish := app.beginSessionNavigationContext()
	defer finish()
	_, err := app.resumeCanonicalSessionForTranscript(tab, tab.Ctrl, sessionRoute(target.Ref().SessionID), 32, false, stale)
	if !errors.Is(err, errSessionNavigationSuperseded) {
		t.Fatalf("stale resume = %v", err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("stale request cancelled the newer navigation: %v", err)
	}
}
