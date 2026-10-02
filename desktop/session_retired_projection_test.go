package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/sessioncatalog"
)

func TestDeletedLegacyTopicStaysHiddenAfterCatalogRebuild(t *testing.T) {
	for _, metadataOnly := range []bool{true, false} {
		t.Run(map[bool]string{true: "lazy", false: "materialized"}[metadataOnly], func(t *testing.T) {
			isolateDesktopUserDirs(t)
			path, _, _ := migrationSingleDAGFixture(t)
			app := newHistoricalLifecycleApp(t)
			if err := app.DeleteTopic("deleted-topic"); err != nil {
				t.Fatal(err)
			}
			catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: metadataOnly, DisableRepair: true})
			if err != nil {
				t.Fatal(err)
			}
			app.sessionCatalog.Store(catalog)
			t.Cleanup(func() { app.desktopSessions.readSnapshots.close(); app.stopSessionCatalog(time.Second) })
			if err := catalog.UpsertSession(t.Context(), sessioncatalog.SessionRecord{Path: path, Directory: config.SessionDir(), Scope: "global", TopicID: "deleted-topic", TopicTitle: "Deleted", OrdinaryVisible: true, Health: sessioncatalog.HealthOK}); err != nil {
				t.Fatal(err)
			}
			if err := app.syncSessionCatalogMetadata(t.Context(), catalog); err != nil {
				t.Fatal(err)
			}
			page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			defer app.ReleaseReadSnapshot(page.SnapshotID)
			if len(page.Items) != 0 {
				t.Fatalf("deleted topic reappeared: %+v", page.Items)
			}
		})
	}
}

func TestAdoptedPathAliasSuppressesExplicitLegacyHead(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, legacy, _ := migrationSingleDAGFixture(t)
	app := newHistoricalLifecycleApp(t)
	catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	app.sessionCatalog.Store(catalog)
	t.Cleanup(func() { app.desktopSessions.readSnapshots.close(); app.stopSessionCatalog(time.Second) })
	if err := catalog.ReconcileDirectory(t.Context(), sessioncatalog.DirectoryTarget{Path: config.SessionDir(), Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	page, err := app.unadoptedLegacyTopics(ProjectTopicPageRequest{Scope: "global"}, map[string]bool{sessionRuntimeKey(path): true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("adopted path reappeared under an explicit head: %+v", page.Items)
	}
	// A later independent fork must remain visible even when an older writer
	// recorded the selected source only by path.
	if _, err := legacy.ForkHead(path, legacy.Snapshot()[1].ID, agent.HeadKindFork, "child"); err != nil {
		t.Fatal(err)
	}
	legacy.Add(provider.Message{ID: "child-message", Role: provider.RoleUser, Content: "independent branch"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	page, err = app.unadoptedLegacyTopics(ProjectTopicPageRequest{Scope: "global"}, map[string]bool{sessionRuntimeKey(path): true}, nil)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("path alias hid independent heads: %+v %v", page.Items, err)
	}
}

func TestMissingProjectDoesNotResurrectDeletedTopic(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, "Removed project"); err != nil {
		t.Fatal(err)
	}
	app := newHistoricalLifecycleApp(t)
	if err := app.DeleteTopic("deleted"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	for _, metadataOnly := range []bool{true, false} {
		catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: metadataOnly, DisableRepair: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := catalog.Close(closeCtx); err != nil {
				t.Errorf("close catalog fixture: %v", err)
			}
		})
		app.sessionCatalog.Store(catalog)
		for _, id := range []string{"deleted", "retained"} {
			if err := catalog.UpsertSession(t.Context(), sessioncatalog.SessionRecord{Path: filepath.Join(desktopSessionDir(root), id+".jsonl"), Directory: desktopSessionDir(root), Scope: "project", WorkspaceRoot: root, TopicID: id, TopicTitle: id, OrdinaryVisible: true, Health: sessioncatalog.HealthOK}); err != nil {
				t.Fatal(err)
			}
		}
		page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].TopicID != "retained" || page.NextCursor != "" {
			t.Fatalf("deleted/offline distinction lost: %+v", page)
		}
		app.ReleaseReadSnapshot(page.SnapshotID)
		app.stopSessionCatalog(time.Second)
		// stopSessionCatalog has a bounded shutdown window; join the catalog's
		// actual close before TempDir cleanup removes its SQLite file on Windows.
		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = catalog.Close(closeCtx)
		cancel()
		if err != nil {
			t.Fatalf("close catalog fixture: %v", err)
		}
	}
}

func TestPurgedLegacyImportStaysHiddenAfterRestart(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, _, _ := migrationSingleDAGFixture(t)
	app := newHistoricalLifecycleApp(t)
	if _, err := app.ListHistoricalSessions(); err != nil {
		t.Fatal(err)
	}
	result, err := app.ImportHistoricalSession(desktopSourceKey(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.ArchiveCanonicalSession(result.Session); err != nil {
		t.Fatal(err)
	}
	if err := app.PurgeCanonicalSession(result.Session); err != nil {
		t.Fatal(err)
	}
	app.stopHistoricalImports()
	app.closeSessionServices()
	for _, metadataOnly := range []bool{true, false} {
		restarted := newHistoricalLifecycleApp(t)
		catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: metadataOnly, DisableRepair: true})
		if err != nil {
			t.Fatal(err)
		}
		restarted.sessionCatalog.Store(catalog)
		t.Cleanup(func() { restarted.desktopSessions.readSnapshots.close(); restarted.stopSessionCatalog(time.Second) })
		if err := catalog.ReconcileDirectory(t.Context(), sessioncatalog.DirectoryTarget{Path: config.SessionDir(), Scope: "global"}); err != nil {
			t.Fatal(err)
		}
		page, err := restarted.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 {
			t.Fatalf("purged source reappeared after restart: %+v", page.Items)
		}
		restarted.ReleaseReadSnapshot(page.SnapshotID)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained migration source was removed: %v", err)
		}
	}
}
