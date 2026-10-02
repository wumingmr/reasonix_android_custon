package main

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/sessioncatalog"
	"reasonix/internal/sqliteuri"
)

func TestStopSessionCatalogWaitsForEarlierTimedOutClose(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	path := filepath.Join(t.TempDir(), "catalog.sqlite")
	catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{Path: path, DisableRepair: true})
	if err != nil {
		t.Fatal(err)
	}
	app.sessionCatalog.Store(catalog)
	t.Cleanup(func() {
		if !app.stopSessionCatalog(sessionCatalogTestDeadline) {
			t.Error("session catalog did not stop before directory cleanup")
		}
	})

	// A reader holding a WAL snapshot makes the closing TRUNCATE checkpoint wait
	// out its busy timeout, so the close outlives a short stop deadline.
	dsn, err := sqliteuri.Disk(path, url.Values{"mode": {"ro"}})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	tx, err := reader.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	var revision uint64
	if err := tx.QueryRow(`SELECT revision FROM catalog_state WHERE id=1`).Scan(&revision); err != nil {
		t.Fatal(err)
	}

	if app.stopSessionCatalog(time.Millisecond) {
		t.Fatal("stop finished while a reader pinned the WAL checkpoint")
	}
	if !app.stopSessionCatalog(sessionCatalogTestDeadline) {
		t.Fatal("a later stop did not wait out the earlier close")
	}
	if state := catalog.Status().State; state != sessioncatalog.StateClosed {
		t.Fatalf("stop reported the catalog stopped while it was %q", state)
	}
}
