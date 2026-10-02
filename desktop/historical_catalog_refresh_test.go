package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/historywork"
)

func TestHistoricalCatalogPublishesBoundedDiscoveryBeforeCompletion(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := newHistoricalLifecycleApp(t)
	root := config.SessionStoreDir()
	for i := range historywork.BatchEntries + 1 {
		path := filepath.Join(root, fmt.Sprintf("progress-%04d", i))
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		// An identifiable but damaged source is checked in bounded batches;
		// its recovery evidence survives cancellation without becoming a row.
		if err := os.WriteFile(filepath.Join(path, "manifest.json"), []byte("{unread"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	notifications := 0
	firstCount := 0
	app.projectTreeChangedHook = func() {
		notifications++
		app.historicalImports.mu.Lock()
		count := len(app.historicalImports.catalog)
		app.historicalImports.mu.Unlock()
		firstCount = count
		if count == 0 || count > historywork.BatchEntries {
			t.Errorf("first publication count=%d", count)
		}
		cancel()
	}
	_, err := app.discoverHistoricalSessions(ctx, false)
	if !errors.Is(err, context.Canceled) || notifications != 1 {
		t.Fatalf("canceled partial discovery: notifications=%d err=%v", notifications, err)
	}
	app.historicalImports.mu.Lock()
	defer app.historicalImports.mu.Unlock()
	if len(app.historicalImports.catalog) != firstCount {
		t.Fatal("cancellation removed previously published entries")
	}
	for _, entry := range app.historicalImports.catalog {
		if entry.node.Health != "unavailable" {
			t.Fatal("damaged metadata was advertised as a usable session")
		}
	}
}

func refreshHistoricalCatalogForTest(app *App) {
	c := &app.historicalImports
	c.mu.Lock()
	c.catalogEnabled = true
	c.catalogAt = time.Time{}
	c.mu.Unlock()
	app.requestHistoricalCatalog()
	c.workers.Wait()
}

func TestHistoricalCatalogRefreshDoesNotInvalidateUnchangedHistory(t *testing.T) {
	isolateDesktopUserDirs(t)
	for _, id := range []string{"unchanged-history", "second-history", "third-history"} {
		coldV4MigrationFixture(t, config.SessionStoreDir(), id)
	}
	app := newHistoricalLifecycleApp(t)
	if _, err := app.ListHistoricalSessions(); err != nil {
		t.Fatal(err)
	}
	var notifications, scans atomic.Int32
	app.projectTreeChangedHook = func() { notifications.Add(1) }
	app.projectTreeCatalogRefreshHook = func() { scans.Add(1) }
	for range 3 {
		refreshHistoricalCatalogForTest(app)
	}
	if notifications.Load() != 0 || scans.Load() != 0 {
		t.Fatalf("unchanged discovery invalidated history: notifications=%d broad scans=%d", notifications.Load(), scans.Load())
	}
}

func TestHistoricalCatalogRefreshPublishesChangesWithoutRescanningLegacyCatalog(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := newHistoricalLifecycleApp(t)
	if _, err := app.ListHistoricalSessions(); err != nil {
		t.Fatal(err)
	}
	var notifications, scans atomic.Int32
	app.projectTreeChangedHook = func() { notifications.Add(1) }
	app.projectTreeCatalogRefreshHook = func() { scans.Add(1) }
	root := config.SessionStoreDir()
	coldV4MigrationFixture(t, root, "new-history")
	refreshHistoricalCatalogForTest(app)
	if notifications.Load() != 1 || scans.Load() != 0 {
		t.Fatalf("discovery must publish only its changed view: notifications=%d broad scans=%d", notifications.Load(), scans.Load())
	}
	if err := os.Rename(filepath.Join(root, "new-history"), filepath.Join(t.TempDir(), "removed-history")); err != nil {
		t.Fatal(err)
	}
	refreshHistoricalCatalogForTest(app)
	if notifications.Load() != 2 || scans.Load() != 0 {
		t.Fatalf("source removal did not publish exactly once: notifications=%d broad scans=%d", notifications.Load(), scans.Load())
	}
}

func TestHistoricalCatalogFailedRefreshDoesNotImmediatelyRetry(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := newHistoricalLifecycleApp(t)
	path := app.workspaceRegistry().Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	var notifications atomic.Int32
	app.projectTreeChangedHook = func() { notifications.Add(1) }
	refreshHistoricalCatalogForTest(app)
	c := &app.historicalImports
	c.mu.Lock()
	at := c.catalogAt
	c.mu.Unlock()
	if at.IsZero() || notifications.Load() != 0 {
		t.Fatalf("failed discovery must settle without a refresh loop: attempt=%v notifications=%d", at, notifications.Load())
	}
	app.requestHistoricalCatalog()
	c.workers.Wait()
	c.mu.Lock()
	retried := c.catalogAt != at
	c.mu.Unlock()
	if retried {
		t.Fatal("failed discovery retried immediately")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	coldV4MigrationFixture(t, config.SessionStoreDir(), "recovered-history")
	refreshHistoricalCatalogForTest(app)
	if notifications.Load() != 1 || len(app.GetHistoricalImportStatus().Items) != 1 {
		t.Fatal("settling a failed refresh prevented later discovery")
	}
}

func TestHistoricalCatalogStartupOwnsDiscoveryAdmission(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := newHistoricalLifecycleApp(t)
	c := &app.historicalImports
	c.discoveryMu.Lock()
	app.startDesktopSessionMigration(t.Context())
	c.mu.Lock()
	pending := c.discoveryPending
	c.mu.Unlock()
	// Release the blocked worker before any assertion can run cleanup.
	c.discoveryMu.Unlock()
	c.workers.Wait()
	if !pending {
		t.Fatal("startup discovery admitted a second sidebar refresh")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.discoveryPending {
		t.Fatal("startup discovery never released refresh admission")
	}
}

func TestHistoricalCatalogRefreshPublishesExternalPresentationChanges(t *testing.T) {
	isolateDesktopUserDirs(t)
	coldV4MigrationFixture(t, config.SessionStoreDir(), "renamed-history")
	app := newHistoricalLifecycleApp(t)
	id := historicalLifecycleID(t, app, "renamed-history")
	var notifications, scans atomic.Int32
	app.projectTreeChangedHook = func() { notifications.Add(1) }
	app.projectTreeCatalogRefreshHook = func() { scans.Add(1) }
	other := newHistoricalLifecycleApp(t)
	if err := other.saveHistoricalSourcePresentation(id, func(p *historicalSourcePresentation) { p.Title = "Changed externally" }); err != nil {
		t.Fatal(err)
	}
	refreshHistoricalCatalogForTest(app)
	refreshHistoricalCatalogForTest(app)
	if notifications.Load() != 1 || scans.Load() != 0 {
		t.Fatalf("external presentation must publish once without index work: notifications=%d scans=%d", notifications.Load(), scans.Load())
	}
	if status := app.GetHistoricalImportStatus(); len(status.Items) != 1 || status.Items[0].Title != "Changed externally" {
		t.Fatalf("external title not reflected: %+v", status)
	}
}

func TestHistoricalCatalogRefreshPublishesExternalImport(t *testing.T) {
	isolateDesktopUserDirs(t)
	coldV4MigrationFixture(t, config.SessionStoreDir(), "externally-imported")
	app := newHistoricalLifecycleApp(t)
	id := historicalLifecycleID(t, app, "externally-imported")
	var notifications, scans atomic.Int32
	app.projectTreeChangedHook = func() { notifications.Add(1) }
	app.projectTreeCatalogRefreshHook = func() { scans.Add(1) }
	other := newHistoricalLifecycleApp(t)
	if _, err := other.StartHistoricalImport([]string{id}); err != nil {
		t.Fatal(err)
	}
	status := awaitHistoricalBatch(t, other)
	if len(status.Items) != 1 || status.Items[0].Status != "imported" {
		t.Fatalf("external import failed: %+v", status)
	}
	refreshHistoricalCatalogForTest(app)
	refreshHistoricalCatalogForTest(app)
	if notifications.Load() != 1 || scans.Load() != 0 {
		t.Fatalf("external import must publish once without index work: notifications=%d scans=%d", notifications.Load(), scans.Load())
	}
	status = app.GetHistoricalImportStatus()
	if len(status.Items) != 1 || status.Items[0].Status != "imported" {
		t.Fatalf("external import not reflected: %+v", status)
	}
}
