package main

import (
	"context"
	"errors"
	"testing"

	"reasonix/internal/boot"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/session"
)

func TestOpenSessionSelectsLocalTabAfterRemote(t *testing.T) {
	app, _, target, _, _ := canonicalWorkspaceOpenFixture(t)
	app.remoteTabMu.Lock()
	app.remoteTabs = map[string]*remoteTab{
		"remote": {id: "remote", ref: RemoteTabRef{HostID: "box", Workspace: "/work"}, state: "disconnected"},
	}
	app.remoteTabLayout = remoteTabLayoutState{activeID: "remote", order: []string{"remote"}, stripOrder: []string{"remote"}}
	app.remoteTabMu.Unlock()

	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatal(err)
	}
	localActive, remoteActive := false, false
	for _, tab := range app.ListTabs() {
		if tab.Remote == nil && tab.SessionID == target.Ref().SessionID {
			localActive = tab.Active
		}
		if tab.Remote != nil {
			remoteActive = tab.Active
		}
	}
	if !localActive || remoteActive {
		t.Fatalf("OpenSession left localActive=%v remoteActive=%v", localActive, remoteActive)
	}
}

func TestCanonicalNavigationLastRequestWins(t *testing.T) {
	app, tab, target, _, _ := canonicalWorkspaceOpenFixture(t)
	ctrl := tab.Ctrl
	tab.Ctrl = &activeNavigationController{SessionAPI: ctrl, IdentityLifecycle: ctrl.(control.IdentityLifecycle), status: control.RuntimeStatus{Running: true}}
	source := session.SessionRef{HostID: localDesktopHostID, SessionID: tab.SessionID}
	app.runtimeRebuildMu.Lock()
	locked := true
	defer func() {
		if locked {
			app.runtimeRebuildMu.Unlock()
		}
	}()
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := app.OpenSession(target.Ref()); first <- err }()
	waitFor(t, "first navigation admission", func() bool { return app.desktopSessions.navigationSeq.Load() == 1 })
	go func() { _, err := app.OpenSession(source); second <- err }()
	waitFor(t, "second navigation admission", func() bool { return app.desktopSessions.navigationSeq.Load() == 2 })
	app.runtimeRebuildMu.Unlock()
	locked = false
	if err := <-first; !errors.Is(err, errSessionNavigationSuperseded) {
		t.Fatalf("first open = %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("last open = %v", err)
	}
	if tab.SessionID != source.SessionID {
		t.Fatal("superseded open won")
	}
}

func TestTicketedCanonicalNavigationKeepsOriginalIntent(t *testing.T) {
	app, source, target, _, _ := canonicalWorkspaceOpenFixture(t)
	app.readyHook = func() {}
	installNoopRuntimeEvents(app, source.sink)
	events := newActivationEventRecorder(app)
	t.Cleanup(func() { app.shutdown(context.Background()) })
	ticket, err := app.StartTopicActivation(TopicActivationRequest{SessionPath: sessionRoute(target.Ref().SessionID)})
	if err != nil {
		t.Fatal(err)
	}
	if got := app.desktopSessions.navigationSeq.Load(); got != 1 {
		t.Fatalf("one navigation claimed %d intents; a nested open can overtake a newer queued request", got)
	}
	events.waitFor(t, activationEventFor(ticket.RequestID, "ready"))
}

func TestDelayedCanonicalNavigationCannotReclaimSupersededIntent(t *testing.T) {
	app, tab, target, _, _ := canonicalWorkspaceOpenFixture(t)
	intent := app.desktopSessions.navigationSeq.Add(1)
	if err := app.SetActiveTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	before := tab.SessionID
	if _, err := app.openTopicSessionWithNavigation("project", tab.WorkspaceRoot, "", sessionRoute(target.Ref().SessionID), intent); !errors.Is(err, errSessionNavigationSuperseded) {
		t.Fatalf("late adopted source open = %v", err)
	}
	if tab.SessionID != before {
		t.Fatal("late source adoption displaced the newer selection")
	}
}

func TestCanonicalOpenReattachesDetachedWorkspace(t *testing.T) {
	app, tab, target, root, workspaceID := canonicalWorkspaceOpenFixture(t)
	ctrl, err := app.buildTabControllerBoot(app.ctx, boot.Options{Model: tab.model, WorkspaceRoot: root, SessionDir: desktopSessionDir(root), Sink: event.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctrl.Close)
	if _, err := ctrl.(control.IdentityLifecycle).OpenSession(t.Context(), target.Ref()); err != nil {
		t.Fatal(err)
	}
	detached := &WorkspaceTab{ID: "detached-B", Scope: "project", WorkspaceRoot: root, SessionID: target.Ref().SessionID, Ready: true, Ctrl: ctrl, model: tab.model, sink: &tabEventSink{tabID: "detached-B", app: app}}
	detached.SessionWorkspace.ID = workspaceID
	key := sessionRuntimeKey(sessionRoute(target.Ref().SessionID))
	app.ensureDetachedSessionsLocked()
	app.detachedSessions[key] = detached
	app.newSessionRuntimeLocked(detached, key)
	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatal(err)
	}
	if tab.Ctrl != ctrl || !sameDesktopPath(tab.WorkspaceRoot, root) || app.detachedSessions[key] != nil {
		t.Fatal("did not transfer the exact detached owner")
	}
	if runtime := app.runtimeBySessionKey[key]; runtime == nil || runtime.Owner != tab {
		t.Fatal("runtime map has a stale owner")
	}
}

func TestCanonicalLegacyResumeUsesTargetWorkspace(t *testing.T) {
	for _, page := range []bool{false, true} {
		t.Run(map[bool]string{false: "messages", true: "page"}[page], func(t *testing.T) {
			app, tab, target, root, _ := canonicalWorkspaceOpenFixture(t)
			tab.HistoricalSource = &SessionSourceRef{Path: "/fixture/old.jsonl"}
			var err error
			if page {
				_, err = app.ResumeSessionPageForTab(tab.ID, sessionRoute(target.Ref().SessionID), 32)
			} else {
				_, err = app.ResumeSessionForTab(tab.ID, sessionRoute(target.Ref().SessionID))
			}
			if err != nil {
				t.Fatal(err)
			}
			if !sameDesktopPath(tab.WorkspaceRoot, root) {
				t.Fatal("compatibility entry kept source workspace")
			}
			if tab.HistoricalSource != nil {
				t.Fatal("compatibility entry retained the preparation action")
			}
		})
	}
}

func TestCanonicalOpenGlobalWorkspace(t *testing.T) {
	app, tab, _, _, _ := canonicalWorkspaceOpenFixture(t)
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: "global-target", CWD: globalWorkspaceRoot(), Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspaceID, target.Ref().SessionID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatal(err)
	}
	if tab.Scope != "global" || tab.SessionWorkspace.ID != workspaceID || !sameDesktopPath(tab.WorkspaceRoot, globalWorkspaceRoot()) {
		t.Fatal("global target inherited project context")
	}
}
