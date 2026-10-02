package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/sessioncatalog"
)

func TestSessionCatalogInitialReconcileSignalFollowsRestoredTabs(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.tabsRestored = make(chan struct{})
	app.startSessionCatalog()
	t.Cleanup(func() { app.stopSessionCatalog(time.Second) })
	_ = waitForSessionCatalogForTest(t, app, nil)

	app.catalogLifecycleMu.Lock()
	done := app.catalogInitialReconcileDone
	app.catalogLifecycleMu.Unlock()
	if done == nil {
		t.Fatal("initial reconcile signal was not armed")
	}
	select {
	case <-done:
		t.Fatal("initial reconcile completed before restored tabs were published")
	default:
	}

	app.markTabsRestored()
	select {
	case <-done:
	case <-time.After(sessionCatalogTestDeadline):
		t.Fatal("initial reconcile did not complete after restored tabs were published")
	}
}

func TestSessionCatalogAdmissionDoesNotWaitForMetadataWriter(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	app.ctx = ctx
	app.tabsRestored = make(chan struct{})
	path := filepath.Join(t.TempDir(), "catalog.sqlite")
	catalog, err := sessioncatalog.Open(ctx, sessioncatalog.Options{Path: path, MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	// Both metadata synchronization and the restored path's database write
	// are held behind a real SQLite writer. Neither belongs to admission.
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "restored.jsonl")
	if err := os.WriteFile(source, []byte("unread body\n"), 0600); err != nil {
		t.Fatal(err)
	}
	app.tabs = map[string]*WorkspaceTab{"current": {ID: "current", Scope: "global", SessionPath: source}}
	admitted := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.watchSessionCatalog(ctx, catalog, nil, func() { close(admitted) })
	}()
	defer func() {
		cancel()
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		select {
		case <-done:
		case <-time.After(sessionCatalogTestDeadline):
			t.Error("catalog watcher did not join shutdown")
		}
	}()
	app.markTabsRestored()
	select {
	case <-admitted:
	case <-time.After(sessionCatalogTestDeadline):
		t.Fatal("startup admission waited for a database writer")
	}
}
