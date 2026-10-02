package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/sessioncatalog"
)

func TestSessionCatalogDeferredCorruptionReplacesOwnerAndRevokesOldReads(t *testing.T) {
	isolateDesktopUserDirs(t)
	path := sessioncatalog.DefaultPath()
	seed, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{Path: path, MetadataOnly: true, StartPaused: true, RevisionFloor: 700})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// This defect is invisible to ordinary metadata queries, but the full
	// integrity audit must still detect it before certifying this generation.
	_, err = db.Exec(`CREATE TABLE integrity_fixture(value INTEGER CHECK(value>=0)); PRAGMA ignore_check_constraints=ON; INSERT INTO integrity_fixture VALUES(-1)`)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	if err := os.MkdirAll(config.SessionDir(), 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(config.SessionDir(), "untouched.jsonl")
	body := []byte("authoritative source must not be repaired by discovery\n")
	if err := os.WriteFile(source, body, 0600); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.tabsRestored = make(chan struct{})
	app.startSessionCatalog()
	t.Cleanup(func() {
		if !app.stopSessionCatalog(5 * time.Second) {
			t.Error("replacement did not stop cleanly")
		}
	})
	old := waitForSessionCatalogForTest(t, app, nil)
	lease, err := old.OpenReadLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if _, err := old.ListOrdinarySessions(lease.Context(t.Context()), sessioncatalog.OrdinaryPageRequest{Scope: "global", Limit: 50}); err != nil {
		t.Fatalf("first metadata page waited for full audit: %v", err)
	}
	app.markTabsRestored()
	select {
	case <-old.Invalidated():
	case <-time.After(sessionCatalogTestDeadline):
		t.Fatal("full background audit did not find corruption")
	}
	replacement := waitForSessionCatalogForTest(t, app, old)
	if status := replacement.Status(); status.Revision <= 700 || status.QuarantinedPath == "" {
		t.Fatalf("replacement lost revision/quarantine evidence: %+v", status)
	}
	if _, err := old.ListOrdinarySessions(lease.Context(context.Background()), sessioncatalog.OrdinaryPageRequest{Scope: "global"}); !errors.Is(err, sessioncatalog.ErrCatalogInvalidated) {
		t.Fatalf("old cursor was not revoked: %v", err)
	}
	reader := &lazyTopicPageReader{catalog: old, lease: lease, positions: map[int]topicPagePosition{0: {}}}
	_, _, readErr := reader.page(t.Context(), 0, 50)
	var operationErr *SessionOperationError
	if !errors.As(readErr, &operationErr) || operationErr.Code != "stale_cursor" {
		t.Fatalf("replaced catalog cursor lost its typed status: %v", readErr)
	}
	got, err := os.ReadFile(source)
	if err != nil || string(got) != string(body) {
		t.Fatalf("metadata rebuild changed original content: %v", err)
	}
	assertSessionCatalogWatcherRunning(t, app)
}
