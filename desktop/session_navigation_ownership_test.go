package main

import (
	"context"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/session"
)

func TestCanonicalNavigationSameWorkspaceBlankRetainsHost(t *testing.T) {
	app, tab, _, _, _ := canonicalWorkspaceOpenFixture(t)
	root, workspace := tab.WorkspaceRoot, tab.SessionWorkspace.ID
	app.acquireSharedHost(root)
	tab.SharedHostKey = root
	original := tab.Ctrl
	active := &activeNavigationController{SessionAPI: original, IdentityLifecycle: original.(control.IdentityLifecycle), status: control.RuntimeStatus{Running: true}}
	tab.Ctrl = active
	t.Cleanup(original.Close)
	target, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: "blank", CWD: root, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspace, target.Ref().SessionID, ""); err != nil {
		t.Fatal(err)
	}
	source := session.SessionRef{HostID: localDesktopHostID, SessionID: tab.SessionID}
	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatal(err)
	}
	if ref, _ := active.SessionRef(); ref != source || tab.Ctrl == active {
		t.Fatal("blank target rebound the active source")
	}
	if refs, ok := sharedHostRefsForTest(t, app, root); !ok || refs != 2 {
		t.Fatalf("both runtimes require a host reference, got %d", refs)
	}
	if _, err := app.OpenSession(source); err != nil {
		t.Fatal(err)
	}
	if refs, ok := sharedHostRefsForTest(t, app, root); !ok || refs != 1 {
		t.Fatalf("idle replacement leaked or released source host, refs=%d", refs)
	}
}

func TestCanonicalNavigationFailureDoesNotDetachActiveSource(t *testing.T) {
	app, tab, target, _, _ := canonicalWorkspaceOpenFixture(t)
	original := tab.Ctrl
	active := &activeNavigationController{SessionAPI: original, IdentityLifecycle: original.(control.IdentityLifecycle), status: control.RuntimeStatus{PendingPrompt: true}}
	tab.Ctrl = active
	before := tab.SessionID
	if err := app.desktopSessionService("").Close(t.Context(), target.Ref()); err != nil {
		t.Fatal(err)
	}
	other, err := session.NewService(localDesktopHostID, session.NewFilesystemPersistence(app.desktopSessions.root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Shutdown(context.Background()) })
	lease, err := other.Open(t.Context(), target.Ref())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release(context.Background()) })
	if _, err := app.OpenSession(target.Ref()); err == nil {
		t.Fatal("writer conflict accepted")
	}
	if tab.Ctrl != active || tab.SessionID != before || active.closed || len(app.detachedSessions) != 0 {
		t.Fatal("failed target changed source ownership")
	}
}
