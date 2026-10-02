package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
)

func TestTaggedDamagedHistoryIsolatesFailureAndRetainsRecoveryEvidence(t *testing.T) {
	for _, version := range []string{"1.38.8", "1.38.9", "1.38.10", "1.38.11"} {
		for _, damage := range []string{"future-revision", "truncated-commit"} {
			t.Run(version+"/"+damage, func(t *testing.T) {
				isolateDesktopUserDirs(t)
				fixture := taggedHistoryFixture(t, version)
				root := config.SessionStoreDir()
				original := copyTaggedDirectory(t, filepath.Join(fixture, "canonical"), root)
				path := filepath.Join(root, "canonical-complete", "manifest.json")
				var body []byte
				if damage == "future-revision" {
					var manifest map[string]any
					readTaggedJSON(t, path, &manifest)
					manifest["storageRevision"] = 999
					body, _ = json.Marshal(manifest)
				} else {
					path = filepath.Join(root, "canonical-complete", "events.frames")
					body = original[path][:len(original[path])-12]
				}
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
				app := NewApp()
				t.Cleanup(app.closeSessionServices)
				healthy := map[string]string{}
				for attempt := range 3 {
					err := app.migrateDesktopSessionsV5(t.Context())
					if attempt == 2 && damage == "truncated-commit" {
						// Export failed after reserving the original source fingerprint.
						// Repair changes that fingerprint. Keep the old operation as
						// conflicting evidence; never certify it against different bytes.
						if !errors.Is(err, workspacestate.ErrMutationConflict) {
							t.Fatalf("changed reserved source lost its conflict: %v", err)
						}
					} else if (attempt < 2) != (err != nil) {
						t.Fatalf("attempt %d: expected isolated failure before repair: %v", attempt, err)
					}
					ledger, err := readDesktopMigrationLedger()
					if err != nil {
						t.Fatal(err)
					}
					for _, name := range []string{"canonical-compacted", "canonical-interrupted"} {
						record := ledger.Records[desktopCanonicalMigrationKey(root, name)]
						if record.Status != "completed" || record.TargetSessionID == "" || (attempt > 0 && healthy[name] != record.TargetSessionID) {
							t.Fatalf("healthy sibling lost or duplicated: %+v", record)
						}
						healthy[name] = record.TargetSessionID
					}
					// Even a torn committed frame is an immutable import source.
					assertMigrationSourceSnapshot(t, map[string][]byte{path: body})
					infos, listErr := listAllCanonicalSessionInfo(t.Context(), app.desktopSessionService("").Query())
					if attempt < 2 && (listErr != nil || len(infos) != 2) {
						t.Fatalf("damaged source was silently published: %d %v", len(infos), listErr)
					}
					if attempt == 1 {
						body = original[path]
						if err := os.WriteFile(path, body, 0600); err != nil {
							t.Fatal(err)
						}
					}
					app.closeSessionServices()
					app = NewApp()
					t.Cleanup(app.closeSessionServices)
				}
				infos, err := listAllCanonicalSessionInfo(t.Context(), app.desktopSessionService("").Query())
				if err != nil || len(infos) != 3 {
					t.Fatalf("repair duplicated or lost sessions: %d %v", len(infos), err)
				}
				assertMigrationSourceSnapshot(t, original)
			})
		}
	}
}
