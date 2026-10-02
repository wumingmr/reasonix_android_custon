package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/session"
	"reasonix/internal/sessioncatalog"
	"reasonix/internal/store"
)

func TestArchivedSourceRecoversAfterHeadIndexPublication(t *testing.T) {
	for _, metadataOnly := range []bool{false, true} {
		name := "materialized"
		if metadataOnly {
			name = "lazy"
		}
		t.Run(name, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			path, _, head := migrationSingleDAGFixture(t)
			app := newHistoricalLifecycleApp(t)
			if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
				t.Fatal(err)
			}
			catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{
				Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: metadataOnly, StartPaused: true, DisableRepair: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			app.sessionCatalog.Store(catalog)
			t.Cleanup(func() { app.desktopSessions.readSnapshots.close(); app.stopSessionCatalog(time.Second) })
			if err := catalog.ReconcileDirectory(t.Context(), sessioncatalog.DirectoryTarget{Path: filepath.Dir(path), Scope: "global"}); err != nil {
				t.Fatal(err)
			}
			state, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			mapping := state.SourceMappings[desktopSourceKey(path, head)]
			ref := session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}
			if err := app.ArchiveCanonicalSession(ref); err != nil {
				t.Fatal(err)
			}
			indexPath := store.SessionEventIndex(path)
			index, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			// Force a sidebar read between invalidation and publication of the
			// head index. The transcript checkpoint does not change during repair.
			if err := os.Remove(indexPath); err != nil {
				t.Fatal(err)
			}
			sourceHeadRows.Delete(path)
			if sourceMappingHasPathAlias(mapping) {
				t.Fatal("missing head index unexpectedly proved single-head ownership")
			}
			if err := os.WriteFile(indexPath, index, 0600); err != nil {
				t.Fatal(err)
			}
			for _, stage := range []string{"archived", "purged"} {
				if stage == "purged" {
					if !metadataOnly {
						if err := app.PurgeCanonicalSession(ref); err != nil {
							t.Fatal(err)
						}
					} else {
						trash, err := app.ListTrashEntries("", "", 50)
						if err != nil || len(trash.Items) != 1 {
							t.Fatalf("trash before clear: %+v %v", trash, err)
						}
						request := SessionLifecycleRequest{OperationID: "clear-cached-source", Action: "purge", ExpectedGeneration: trash.Generation}
						for _, row := range trash.Items {
							request.Targets = append(request.Targets, SessionLifecycleTarget{Ref: row.Ref, WorkspaceID: row.WorkspaceID})
						}
						if result, err := app.ApplySessionLifecycle(request); err != nil || !result.Committed {
							t.Fatalf("clear trash: %+v %v", result, err)
						}
					}
					if state, err := app.workspaceRegistry().Load(t.Context()); err != nil || state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Deleted {
						t.Fatalf("purge did not commit: %+v %v", state.SessionStates[ref.SessionID], err)
					}
				}
				page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
				if err != nil {
					t.Fatal(err)
				}
				app.ReleaseReadSnapshot(page.SnapshotID)
				if len(page.Items) != 0 {
					t.Errorf("%s source reappeared after index repair: %+v", stage, page.Items)
				}
				if rows := app.listSessionsFromDir(filepath.Dir(path), ""); len(rows) != 0 {
					t.Errorf("%s source reappeared in history after index repair: %+v", stage, rows)
				}
			}
		})
	}
}

func TestSourceHeadsRejectCachedIndexAfterLogChanges(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, _, _ := migrationSingleDAGFixture(t)
	heads, err := sessionSourceHeads(path)
	if err != nil || len(heads) != 1 {
		t.Fatalf("initial heads: %+v %v", heads, err)
	}
	logPath := store.SessionEventLog(path)
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a writer committing log bytes before its index publication.
	if err := os.WriteFile(logPath, append(log, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if heads, err := sessionSourceHeads(path); err != nil || len(heads) != 0 {
		t.Fatalf("stale index remained authoritative: %+v %v", heads, err)
	}
	if err := os.WriteFile(logPath, log, 0600); err != nil {
		t.Fatal(err)
	}
	if heads, err := sessionSourceHeads(path); err != nil || len(heads) != 1 {
		t.Fatalf("valid heads did not recover: %+v %v", heads, err)
	}
}

func TestPurgedSourceDoesNotFallBackToUnidentifiedPath(t *testing.T) {
	for _, metadataOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "materialized", true: "lazy"}[metadataOnly], func(t *testing.T) {
			isolateDesktopUserDirs(t)
			path, legacy, head := migrationSingleDAGFixture(t)
			app := newHistoricalLifecycleApp(t)
			if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
				t.Fatal(err)
			}
			state, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			ref := session.SessionRef{HostID: localDesktopHostID, SessionID: state.SourceMappings[desktopSourceKey(path, head)].SessionID}
			if err := app.ArchiveCanonicalSession(ref); err != nil {
				t.Fatal(err)
			}
			if err := app.PurgeCanonicalSession(ref); err != nil {
				t.Fatal(err)
			}
			catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{
				Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: metadataOnly, StartPaused: true, DisableRepair: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			app.sessionCatalog.Store(catalog)
			t.Cleanup(func() { app.desktopSessions.readSnapshots.close(); app.stopSessionCatalog(time.Second) })
			if err := catalog.ReconcileDirectory(t.Context(), sessioncatalog.DirectoryTarget{Path: filepath.Dir(path), Scope: "global"}); err != nil {
				t.Fatal(err)
			}
			assertRows := func(want int) {
				t.Helper()
				page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
				if err != nil {
					t.Fatal(err)
				}
				app.ReleaseReadSnapshot(page.SnapshotID)
				if len(page.Items) != want {
					t.Errorf("sidebar rows = %+v; want %d", page.Items, want)
				}
				if rows := app.listSessionsFromDir(filepath.Dir(path), ""); len(rows) != want {
					t.Errorf("history rows = %+v; want %d", rows, want)
				}
			}
			indexPath := store.SessionEventIndex(path)
			index, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(indexPath); err != nil {
				t.Fatal(err)
			}
			assertRows(0)
			if err := os.WriteFile(indexPath, []byte("incomplete index"), 0600); err != nil {
				t.Fatal(err)
			}
			assertRows(0)
			if err := os.WriteFile(indexPath, index, 0600); err != nil {
				t.Fatal(err)
			}
			assertRows(0)
			// A sibling can be published before catalog reconciliation sees
			// its head count. A flat path row cannot express that identity.
			if _, err := legacy.ForkHead(path, legacy.Snapshot()[1].ID, agent.HeadKindFork, "surviving sibling"); err != nil {
				t.Fatal(err)
			}
			page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			app.ReleaseReadSnapshot(page.SnapshotID)
			if len(page.Items) != 1 || page.Items[0].Source == nil || page.Items[0].Source.HeadID == "" || page.Items[0].Source.HeadID == head {
				t.Fatalf("sibling lost its independent identity: %+v", page.Items)
			}
			assertRows(1)
		})
	}
}

func TestSourceHeadsObserveForkWithoutCheckpointChange(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, legacy, _ := migrationSingleDAGFixture(t)
	checkpoint, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if heads, err := sessionSourceHeads(path); err != nil || len(heads) != 1 {
		t.Fatalf("initial heads: %+v %v", heads, err)
	}
	if _, err := legacy.ForkHead(path, legacy.Snapshot()[1].ID, agent.HeadKindFork, "Independent sibling"); err != nil {
		t.Fatal(err)
	}
	// Checkpoint publication can be separate from the authoritative DAG and
	// head-index writes; restoring its timestamp isolates that boundary.
	if err := os.Chtimes(path, checkpoint.ModTime(), checkpoint.ModTime()); err != nil {
		t.Fatal(err)
	}
	if heads, err := sessionSourceHeads(path); err != nil || len(heads) != 2 {
		t.Fatalf("new sibling hidden behind cached heads: %+v %v", heads, err)
	}
}
