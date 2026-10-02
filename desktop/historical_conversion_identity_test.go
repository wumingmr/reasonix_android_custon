package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"reasonix/internal/session"
)

// Lineage migration carries a legacy head into the receipt for its converted
// single-session directory. Discovery addresses that directory without a head.
func TestHistoricalConvertedHeadKeepsLifecycleAfterRestart(t *testing.T) {
	for _, lifecycle := range []string{workspacestate.Active, workspacestate.Archived, workspacestate.Deleted} {
		t.Run(lifecycle, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			const id = "converted-head"
			root := config.SessionStoreDir()
			old := coldV4MigrationFixture(t, root, id)
			before := migrationSourceSnapshot(t, canonicalMigrationSourceFiles(root, id))
			app := newHistoricalLifecycleApp(t)
			source := desktopMigrationSource{root: root, scope: "global", headID: "main"}
			if err := app.migrateCanonicalSession(t.Context(), old, source, "", id); err != nil {
				t.Fatal(err)
			}
			original, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			ref := session.SessionRef{HostID: localDesktopHostID, SessionID: id}
			if lifecycle != workspacestate.Active {
				if err := app.ArchiveCanonicalSession(ref); err != nil {
					t.Fatal(err)
				}
			}
			if lifecycle == workspacestate.Deleted {
				if err := app.PurgeCanonicalSession(ref); err != nil {
					t.Fatal(err)
				}
				// Older builds retained some purged sources. Recreate that disk
				// layout without changing the durable deletion receipt.
				for path, body := range before {
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, body, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			app.stopHistoricalImports()
			app.closeSessionServices()
			app = newHistoricalLifecycleApp(t)
			if _, err := app.ListHistoricalSessions(); err != nil {
				t.Fatal(err)
			}
			page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
			want := 0
			if lifecycle == workspacestate.Active {
				want = 1
			}
			if lifecycle == workspacestate.Deleted {
				// Recreating the retained directory above changes its filesystem
				// revision. Metadata-only discovery cannot certify it unchanged;
				// explicit archive verifies the original fingerprint once.
				if _, err := app.ArchiveSessionTarget(SessionSelector{Source: &SessionSourceRef{Path: filepath.Join(root, id), SourceKey: desktopSourceKey(filepath.Join(root, id), "")}}); err != nil {
					t.Fatal(err)
				}
				if _, err := app.ListHistoricalSessions(); err != nil {
					t.Fatal(err)
				}
				page, err = app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
			}
			if err != nil || len(page.Items) != want {
				t.Fatalf("converted source reappeared as a new sidebar row: got %d, want %d: %v", len(page.Items), want, err)
			}
			key := desktopSourceKey(filepath.Join(root, id), "")
			result, err := app.ImportHistoricalSession(key)
			if lifecycle == workspacestate.Active {
				if err != nil || result.Session != ref {
					t.Fatalf("directory selector lost existing target: %+v %v", result, err)
				}
				archived, err := app.ArchiveSessionTarget(SessionSelector{Source: &SessionSourceRef{Path: filepath.Join(root, id), SourceKey: key}})
				if err != nil || !archived.Committed {
					t.Fatalf("archive through directory selector: %+v %v", archived, err)
				}
				if err := app.RestoreCanonicalSession(ref); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("directory selector revived a retired conversion")
			}
			state, err := app.workspaceRegistry().Load(t.Context())
			if err != nil || state.SessionStates[id].Lifecycle != lifecycle || !reflect.DeepEqual(state.SourceMappings, original.SourceMappings) {
				t.Fatalf("conversion receipt changed: %v", err)
			}
			assertMigrationSourceSnapshot(t, before)
		})
	}
}
