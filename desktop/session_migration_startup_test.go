package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/identitylock"
	"reasonix/internal/session"
)

func TestStartupReservationReadinessPrecedesRestoredShell(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	app.tabsRestored = make(chan struct{})
	var scans, notifications atomic.Int32
	app.projectTreeCatalogRefreshHook = func() { scans.Add(1) }
	app.projectTreeChangedHook = func() { notifications.Add(1) }
	app.startDesktopSessionMigration(ctx)
	select {
	case <-app.desktopMigrationDone:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("reservation recovery waited for the shell that depends on it")
	}
	app.historicalImports.mu.Lock()
	pending := app.historicalImports.discoveryPending
	app.historicalImports.mu.Unlock()
	if !pending {
		t.Fatal("historical discovery ran before shell readiness")
	}
	app.markTabsRestored()
	app.historicalImports.workers.Wait()
	if scans.Load() != 0 || notifications.Load() == 0 {
		t.Fatalf("startup must notify without scheduling duplicate root scans: scans=%d notifications=%d", scans.Load(), notifications.Load())
	}
	app.closeSessionServices()
}

// Startup may discover historical metadata, but only an explicit open/import
// request is allowed to convert source content or replay a prepared import.
func TestDesktopStartupLeavesColdHistoryForExplicitImport(t *testing.T) {
	for _, scope := range []string{"global", "project"} {
		t.Run(scope, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			root := config.SessionStoreDir()
			if scope == "project" {
				workspace := t.TempDir()
				root = config.ProjectSessionStoreDir(workspace)
				if err := saveProjectsFile(desktopProjectFile{Projects: []desktopProject{{Root: workspace}}}); err != nil {
					t.Fatal(err)
				}
			}
			const id = "startup-cold-history"
			coldV4MigrationFixture(t, root, id)
			original := startupHistorySourceBytes(t, root, id)
			app := NewApp()
			t.Cleanup(app.closeSessionServices)
			startupExistingV5Fixture(t, app)
			runHistoryDiscoveryStartup(t, app)
			ref := session.SessionRef{HostID: localDesktopHostID, SessionID: id}
			if _, err := app.desktopSessionService("").Query().Snapshot(t.Context(), ref); !errors.Is(err, session.ErrSessionNotFound) {
				t.Errorf("startup must not publish historical content without an explicit import: %v", err)
			}
			assertStartupHistorySourceUnchanged(t, root, id, original)
			assertStartupExistingV5Available(t, app)
		})
	}
}

func TestDesktopStartupPreservesPreparedImportWithoutWaitingForSource(t *testing.T) {
	for _, lockName := range []string{"writer", "ownership"} {
		t.Run(lockName, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			root := config.SessionStoreDir()
			const id = "startup-interrupted-import"
			coldV4MigrationFixture(t, root, id)
			original := startupHistorySourceBytes(t, root, id)
			app := NewApp()
			t.Cleanup(app.closeSessionServices)
			startupExistingV5Fixture(t, app)
			workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
			if err != nil {
				t.Fatal(err)
			}
			sourcePath := filepath.Join(root, id)
			fingerprint, err := desktopSourceFingerprint(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			opID, err := app.prepareDesktopImport(t.Context(), desktopMigrationSource{scope: "global"}, sourcePath, fingerprint, id, workspace)
			if err != nil {
				t.Fatal(err)
			}
			before, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			ledger := []byte(`{"version":1,"records":{"interrupted-source":{"sourceKey":"interrupted-source","targetSessionId":"startup-interrupted-import","status":"pending","attempts":4}},"futureField":{"preserve":true}}`)
			if err := os.MkdirAll(filepath.Dir(desktopMigrationLedgerPath()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(desktopMigrationLedgerPath(), ledger, 0o600); err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(sourcePath, "writer.lock")
			if lockName == "ownership" {
				lockPath = filepath.Join(root, "."+id+".ownership.lock")
			}
			release, err := identitylock.Acquire(t.Context(), lockPath)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			runHistoryDiscoveryStartup(t, app)
			after, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.PendingOperations[opID], after.PendingOperations[opID]) {
				t.Error("startup changed a prepared historical import without an explicit request")
			}
			if body := fileBytes(t, desktopMigrationLedgerPath()); !bytes.Equal(body, ledger) {
				t.Error("startup modified the historical ledger, including unrecognized fields")
			}
			assertStartupHistorySourceUnchanged(t, root, id, original)
			assertStartupExistingV5Available(t, app)
		})
	}
}

func runHistoryDiscoveryStartup(t *testing.T, app *App) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	app.ctx = ctx
	app.startDesktopSessionMigration(ctx)
	select {
	case <-app.desktopMigrationDone:
		// Reservation recovery no longer gates on historical discovery. Tests
		// inspecting the eventual directory view join that worker explicitly.
		app.historicalImports.workers.Wait()
	case <-time.After(5 * time.Second):
		// Source locks are deliberately retained throughout startup. This is a
		// deadlock watchdog, not a startup performance requirement.
		if app.runtimeRebuildMu.TryLock() {
			app.runtimeRebuildMu.Unlock()
		} else {
			t.Error("historical startup wait retained the global runtime lock")
		}
		if app.runtimeAdmissionMu.TryRLock() {
			app.runtimeAdmissionMu.RUnlock()
		} else {
			t.Error("historical startup wait blocked runtime admission")
		}
		cancel()
		select {
		case <-app.desktopMigrationDone:
		case <-time.After(5 * time.Second):
			t.Fatal("historical startup ignored cancellation")
		}
		t.Error("startup waited for a historical source lock without an import request")
	}
}

func startupHistorySourceBytes(t *testing.T, root, id string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
	for _, name := range []string{"manifest.json", "events.frames"} {
		out[name] = fileBytes(t, filepath.Join(root, id, name))
	}
	return out
}

func assertStartupHistorySourceUnchanged(t *testing.T, root, id string, before map[string][]byte) {
	t.Helper()
	for name, body := range before {
		if !bytes.Equal(body, fileBytes(t, filepath.Join(root, id, name))) {
			t.Errorf("historical source %s was modified by startup", name)
		}
	}
}

func startupExistingV5Fixture(t *testing.T, app *App) {
	t.Helper()
	workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	service := app.desktopSessionService("")
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "startup-existing-v5", CWD: globalWorkspaceRoot(), Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspace, runtime.Ref().SessionID, ""); err != nil {
		t.Fatal(err)
	}
}

func assertStartupExistingV5Available(t *testing.T, app *App) {
	t.Helper()
	if _, err := app.desktopSessionService("").Query().Snapshot(t.Context(), session.SessionRef{HostID: localDesktopHostID, SessionID: "startup-existing-v5"}); err != nil {
		t.Fatalf("existing v5 session became unavailable: %v", err)
	}
	if !app.runtimeRebuildMu.TryLock() {
		t.Fatal("startup retained the runtime lock")
	}
	app.runtimeRebuildMu.Unlock()
	if !app.runtimeAdmissionMu.TryRLock() {
		t.Fatal("startup retained the runtime admission lock")
	}
	app.runtimeAdmissionMu.RUnlock()
}
