package main

import (
	"testing"
)

// A tab whose controller build was blocked by a held session lease keeps
// Ctrl == nil and StartupErrLeaseHeld set. Every tab-scoped command resolves
// through tabAndCtrlByID, which retries that blocked startup before reporting
// a missing runtime; the local-tab resolver used by OpenSession must do the
// same, or opening a session on the only tab fails until the user finds
// another way to trigger a rebuild.
func TestActiveOrSingleLocalTabRecoversLeaseBlockedStartup(t *testing.T) {
	app, ref := lifecycleFixture(t)
	app.readyHook = func() {}

	release, err := app.beginProjectRuntimeAdmission("global", globalTabWorkspaceRoot())
	if err != nil {
		t.Fatal(err)
	}
	tab := app.createTabEntry("global", globalTabWorkspaceRoot(), "")
	tab.sink = &tabEventSink{tabID: tab.ID, app: app}
	installNoopRuntimeEvents(app, tab.sink)
	// The startup that would have built this tab's runtime was refused by a
	// lease the holder has since released.
	tab.StartupErr = (&sessionLeaseBusyError{}).Error()
	tab.StartupErrLeaseHeld = true
	tab.Ready = false
	app.publishRestoredTab(tab, release)
	t.Cleanup(func() {
		if ctrl := app.controllerForTab(tab); ctrl != nil {
			ctrl.Close()
		}
		tab.releaseSessionLease()
	})

	resolved, ctrl := app.activeOrSingleLocalTab()
	if resolved != tab {
		t.Fatalf("resolved tab = %+v, want the lease-blocked tab", resolved)
	}
	if ctrl == nil {
		t.Fatal("lease-blocked startup was not recovered; the tab stayed without a runtime")
	}
	if _, err := app.OpenSession(ref); err != nil {
		t.Fatalf("open session on the recovered tab: %v", err)
	}
	app.mu.RLock()
	boundSessionID := app.tabs[tab.ID].SessionID
	app.mu.RUnlock()
	if boundSessionID != ref.SessionID {
		t.Fatalf("tab session after open = %q, want %q", boundSessionID, ref.SessionID)
	}
}
