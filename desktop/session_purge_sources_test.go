package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"reasonix/internal/identitylock"
	"reasonix/internal/session"
)

func TestPurgeMigratedSourcesProtectsOtherOwnersAndChangedContent(t *testing.T) {
	for _, scenario := range []string{"exclusive", "active_owner", "archived_owner", "pending_import", "recovery", "changed", "older_purge", "missing_root"} {
		t.Run(scenario, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			root := config.SessionStoreDir()
			coldV4MigrationFixture(t, root, "source")
			app := newHistoricalLifecycleApp(t)
			key := historicalLifecycleID(t, app, "source")
			result, err := app.ImportHistoricalSession(key)
			if err != nil {
				t.Fatal(err)
			}
			state, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			mapping := state.SourceMappings[key]
			var otherID string
			switch scenario {
			case "active_owner", "archived_owner":
				other := addLifecycleFixtureSession(t, app, "other-owner")
				otherID = other.SessionID
				shared := mapping
				shared.SourceKey += ":review:other"
				shared.SessionID = otherID
				if err := app.workspaceRegistry().RecordSource(t.Context(), shared, workspacestate.Presentation{}); err != nil {
					t.Fatal(err)
				}
				if scenario == "archived_owner" {
					if err := app.ArchiveCanonicalSession(other); err != nil {
						t.Fatal(err)
					}
				}
			case "pending_import":
				pending := mapping
				pending.SourceKey, pending.SessionID = key+":review:pending", "pending-owner"
				if err := app.workspaceRegistry().BeginOperation(t.Context(), workspacestate.Operation{ID: "pending-source-import", Kind: "import", Lifecycle: workspacestate.Active, Mapping: &pending}); err != nil {
					t.Fatal(err)
				}
			case "recovery":
				if err := app.sourceRecovery(t.Context(), mapping.Path, "canonical", "source_changed_after_adoption", "global", ""); err != nil {
					t.Fatal(err)
				}
			case "changed":
				// A prior application continued the old source after migration.
				path := filepath.Join(mapping.Path, "events.frames")
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(body, []byte("later source bytes")...), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := app.ArchiveCanonicalSession(result.Session); err != nil {
				t.Fatal(err)
			}
			if scenario == "older_purge" {
				state, err = app.workspaceRegistry().Load(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if err := app.workspaceRegistry().BeginPurge(t.Context(), result.Session.SessionID, state.Generation); err != nil {
					t.Fatal(err)
				}
			}
			before, err := desktopSourceFingerprint(mapping.Path)
			if err != nil {
				t.Fatal(err)
			}
			verifyPath := mapping.Path
			if scenario == "missing_root" {
				if err := os.Rename(root, root+"-offline"); err != nil {
					t.Fatal(err)
				}
				verifyPath = filepath.Join(root+"-offline", "source")
			}
			if err := app.PurgeCanonicalSession(result.Session); err != nil {
				t.Fatal(err)
			}
			if scenario == "exclusive" {
				if _, err := os.Stat(mapping.Path); !os.IsNotExist(err) {
					t.Fatalf("exclusive original retained: %v", err)
				}
			} else if after, err := desktopSourceFingerprint(verifyPath); err != nil || after != before {
				t.Fatalf("protected original changed: %v", err)
			}
			state, err = app.workspaceRegistry().Load(t.Context())
			if err != nil || workspacestate.ClassifyPurge(state, result.Session.SessionID) != workspacestate.PurgeCommitted {
				t.Fatalf("purge incomplete: %v", err)
			}
			if state.SourceMappings[key].SessionID != result.Session.SessionID {
				t.Fatal("deletion lost source tombstone")
			}
			if otherID != "" && state.SessionStates[otherID].Lifecycle == workspacestate.Deleted {
				t.Fatal("purge deleted another source owner")
			}
			if otherID != "" {
				other := session.SessionRef{HostID: localDesktopHostID, SessionID: otherID}
				if scenario == "active_owner" {
					if err := app.ArchiveCanonicalSession(other); err != nil {
						t.Fatal(err)
					}
				}
				if err := app.PurgeCanonicalSession(other); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(mapping.Path); !os.IsNotExist(err) {
					t.Fatalf("last source owner left the exclusive original: %v", err)
				}
			}
		})
	}
}

func TestPurgeMigratedSourcesBusyWriterResumesFromTombstone(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	coldV4MigrationFixture(t, root, "busy-source")
	app := newHistoricalLifecycleApp(t)
	key := historicalLifecycleID(t, app, "busy-source")
	result, err := app.ImportHistoricalSession(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.ArchiveCanonicalSession(result.Session); err != nil {
		t.Fatal(err)
	}
	release, err := identitylock.TryAcquire(filepath.Join(root, "busy-source", "writer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := app.PurgeCanonicalSession(result.Session); !errors.Is(err, identitylock.ErrHeld) {
		t.Fatalf("purge ignored source writer: %v", err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil || workspacestate.ClassifyPurge(state, result.Session.SessionID) != workspacestate.PurgeTombstoned {
		t.Fatalf("missing resumable tombstone: %v", err)
	}
	var plan workspacestate.PurgeSourceCleanup
	if err := json.Unmarshal(state.PendingOperations["purge-"+result.Session.SessionID].Request, &plan); err != nil || len(plan.Sources) != 1 {
		t.Fatalf("source cleanup intent lost: %+v %v", plan, err)
	}
	release()
	app.closeSessionServices()
	app = newHistoricalLifecycleApp(t)
	if err := app.recoverDesktopSessionOperations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "busy-source")); !os.IsNotExist(err) {
		t.Fatalf("resumed purge retained source: %v", err)
	}
	if _, err := app.ImportHistoricalSession(key); err == nil {
		t.Fatal("purged source was imported again")
	}
}

func TestPurgeMigratedSourcesCrashRestart(t *testing.T) {
	for _, phase := range []string{"after-tombstone", "before-source-cleanup", "after-source-cleanup"} {
		t.Run(phase, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			root := config.SessionStoreDir()
			coldV4MigrationFixture(t, root, "crash-source")
			app := newHistoricalLifecycleApp(t)
			key := historicalLifecycleID(t, app, "crash-source")
			result, err := app.ImportHistoricalSession(key)
			if err != nil {
				t.Fatal(err)
			}
			if err := app.ArchiveCanonicalSession(result.Session); err != nil {
				t.Fatal(err)
			}
			req := lifecycleRequest(t, app, result.Session, "source-crash", "purge")
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			app.stopHistoricalImports()
			app.closeSessionServices()
			for _, checkpoint := range []string{phase, "", ""} {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPurgeCommandCrashHelper$")
				cmd.Env = append(os.Environ(), "REASONIX_PURGE_APP_ROOT="+app.desktopSessions.root, "REASONIX_PURGE_APP_REGISTRY="+app.workspaceRegistry().Path(), "REASONIX_PURGE_APP_REQUEST="+string(body), "REASONIX_PURGE_APP_POINT="+checkpoint)
				output, err := cmd.CombinedOutput()
				cancel()
				var exit *exec.ExitError
				if checkpoint != "" {
					if !errors.As(err, &exit) || exit.ExitCode() != 23 {
						t.Fatalf("source checkpoint not reached: %v %s", err, output)
					}
				} else if err != nil {
					t.Fatalf("source recovery failed: %v %s", err, output)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "crash-source")); !os.IsNotExist(err) {
				t.Fatalf("original survived crash recovery: %v", err)
			}
			state, err := workspacestate.NewStore(app.workspaceRegistry().Path()).Load(t.Context())
			if err != nil || state.SourceMappings[key].SessionID != result.Session.SessionID || workspacestate.ClassifyPurge(state, result.Session.SessionID) != workspacestate.PurgeCommitted {
				t.Fatalf("recovery lost deletion evidence: %v", err)
			}
		})
	}
}
