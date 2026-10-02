package main

import (
	"testing"
	"time"
)

func TestBlankReuseWaitsForPendingCanonicalCreateInsteadOfCancellingIt(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	done, cancelled := make(chan struct{}), make(chan struct{}, 1)
	tab := &WorkspaceTab{ID: "starting-blank", Scope: "global", model: "fixture/model", buildDone: done,
		buildGeneration: 1, buildDoneGen: 1, buildCancel: func() { cancelled <- struct{}{} }}
	app.tabs[tab.ID], app.tabOrder, app.activeTabID = tab, []string{tab.ID}, tab.ID
	result := make(chan error, 1)
	go func() { result <- app.alignReusableBlankTabModel(tab, "fixture/model") }()
	// Keep the original publisher at the durable-content -> registry boundary.
	// It must finish before reuse can return; cancellation here reproduces the
	// pending create that used to become an extra empty row after restart.
	select {
	case <-cancelled:
		t.Error("reuse cancelled an identical in-flight canonical create")
	case err := <-result:
		t.Fatalf("reuse returned before its original publisher completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	app.mu.Lock()
	tab.Ctrl, tab.SessionID, tab.Ready = &activationStubController{}, "original-pending-session", true
	if tab.buildDone == done {
		close(done)
		tab.buildDone = nil
	}
	app.mu.Unlock()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reuse did not resume after canonical publication")
	}
	if tab.SessionID != "original-pending-session" || tab.buildGeneration != 1 {
		t.Fatalf("reuse replaced the original create: session=%s generation=%d", tab.SessionID, tab.buildGeneration)
	}
}
