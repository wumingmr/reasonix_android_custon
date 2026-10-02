package main

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

func TestHistoricalDiscoveryResumesPublishedRepair(t *testing.T) {
	for _, replay := range []string{"discovery", "journal"} {
		t.Run(replay, func(t *testing.T) { checkHistoricalPublishedRepair(t, replay) })
	}
}

func checkHistoricalPublishedRepair(t *testing.T, replay string) {
	t.Helper()
	app, selector, _ := historicalArchiveFixture(t)
	result, err := app.ImportHistoricalSession(selector.Source.SourceKey)
	if err != nil {
		t.Fatal(err)
	}
	title, pinned := "Preserve on restart", true
	if err := app.workspaceRegistry().UpdatePresentation(t.Context(), []string{result.Session.SessionID}, &title, &pinned); err != nil {
		t.Fatal(err)
	}
	app.stopHistoricalImports()
	app.closeSessionServices()
	if err := os.Rename(filepath.Join(app.desktopSessions.root, result.Session.SessionID), filepath.Join(t.TempDir(), "removed")); err != nil {
		t.Fatal(err)
	}
	app = newHistoricalLifecycleApp(t)
	app.lifecycleCheckpointHook = func(phase string) {
		if phase == "historical-repair-content-published" {
			panic("simulated exit")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("missing interruption")
			}
		}()
		_, _ = app.ListHistoricalSessions()
	}()
	app.stopHistoricalImports()
	app.closeSessionServices()
	app = newHistoricalLifecycleApp(t)
	if replay == "journal" {
		if err := app.recoverDesktopSessionOperations(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.ListHistoricalSessions(); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if pendingHistoricalOperation(state, selector.Source.SourceKey) != nil {
		t.Fatal("repair journal was not completed")
	}
	if p := state.Presentation[result.Session.SessionID]; p.Title != title || !p.Pinned {
		t.Fatalf("lost presentation: %+v", p)
	}
	if err := app.ArchiveCanonicalSession(result.Session); err != nil {
		t.Fatal(err)
	}
	if err := app.PurgeCanonicalSession(result.Session); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalDiscoveryRepairsMissingLegacyDestination(t *testing.T) {
	for _, damage := range []string{"mapping", "target", "source-changed"} {
		t.Run(damage, func(t *testing.T) { checkHistoricalLegacyRepair(t, damage) })
	}
}

func checkHistoricalLegacyRepair(t *testing.T, damage string) {
	t.Helper()
	isolateDesktopUserDirs(t)
	path, _, head := migrationSingleDAGFixture(t)
	app := newHistoricalLifecycleApp(t)
	workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.migrateLegacySession(t.Context(), path, desktopMigrationSource{scope: "global", headID: head}, workspace); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mapping := state.SourceMappings[desktopSourceKey(path, head)]
	app.stopHistoricalImports()
	app.closeSessionServices()
	if damage != "mapping" {
		if err := os.Rename(filepath.Join(app.desktopSessions.root, mapping.SessionID), filepath.Join(t.TempDir(), "removed")); err != nil {
			t.Fatal(err)
		}
	} else {
		rewriteHistoricalRegistry(t, app, func(state *workspacestate.State) {
			state.SourceMappings = map[string]workspacestate.SourceMapping{}
			state.PendingOperations = map[string]workspacestate.Operation{}
		})
	}
	app = newHistoricalLifecycleApp(t)
	if damage == "source-changed" {
		app.lifecycleCheckpointHook = func(phase string) {
			if phase != "historical-repair-source-exported" {
				return
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if damage == "mapping" {
		// The historical startup pass can precede first catalog discovery.
		// Its completion must recover the receipt without a management action.
		installSessionCatalogForTest(t, app, filepath.Dir(path), "global", "")
		app.historicalImports.mu.Lock()
		app.historicalImports.initialize(t.Context())
		app.historicalImports.catalogEnabled = true
		app.historicalImports.mu.Unlock()
		app.requestHistoricalLegacyReconciliation(t.Context())
		app.historicalImports.workers.Wait()
	} else if _, err := app.ListHistoricalSessions(); err != nil {
		t.Fatal(err)
	}
	if damage == "source-changed" {
		if _, err := os.Stat(filepath.Join(app.desktopSessions.root, mapping.SessionID)); !os.IsNotExist(err) {
			t.Fatalf("published stale source content: %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal("source was not retained", err)
		}
		return
	}
	state, err = app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if restored, found, err := state.ResolveSource(desktopSourceKey(path, head)); err != nil || !found || restored.SessionID != mapping.SessionID {
		t.Fatalf("legacy receipt not repaired: %+v %v", restored, err)
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}
	if _, err := app.desktopSessionService("").Query().Snapshot(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if err := app.ArchiveCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	if err := app.PurgeCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	state, err = app.workspaceRegistry().Load(t.Context())
	if err != nil || state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Deleted {
		t.Fatalf("legacy destination not deleted: %v", err)
	}
}
