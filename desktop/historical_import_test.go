package main

import (
	"errors"
	"path/filepath"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/identitylock"
)

func TestHistoricalImportIsExplicitAndSourceBusyDoesNotTakeRuntimeGate(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	coldV4MigrationFixture(t, root, "on-demand")
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	list, err := app.ListHistoricalSessions()
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("discovery: %+v %v", list, err)
	}
	before := startupHistorySourceBytes(t, root, "on-demand")
	release, err := identitylock.Acquire(t.Context(), filepath.Join(root, ".on-demand.ownership.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = app.ImportHistoricalSession(list.Items[0].ID)
	if !errors.Is(err, errHistoricalSourceBusy) {
		t.Fatalf("busy source: %v", err)
	}
	if !app.runtimeRebuildMu.TryLock() {
		t.Fatal("historical import retained runtime gate")
	}
	app.runtimeRebuildMu.Unlock()
	release()
	result, err := app.ImportHistoricalSession(list.Items[0].ID)
	if err != nil || result.Session.SessionID == "" {
		t.Fatalf("import: %+v %v", result, err)
	}
	again, err := app.ImportHistoricalSession(list.Items[0].ID)
	if err != nil || again.Session != result.Session {
		t.Fatalf("duplicate: %+v %v", again, err)
	}
	assertStartupHistorySourceUnchanged(t, root, "on-demand", before)
}
