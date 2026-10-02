package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/config"
)

func TestHistoricalRestoredTabActivationKeepsPreparationExplicit(t *testing.T) {
	for _, activate := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct-build", true: "tab-selection"}[activate], func(t *testing.T) {
			isolateDesktopUserDirs(t)
			app := newHistoricalLifecycleApp(t)
			t.Cleanup(func() { app.shutdown(context.Background()) })
			release, err := app.beginProjectRuntimeAdmission("global", globalTabWorkspaceRoot())
			if err != nil {
				t.Fatal(err)
			}
			tab := app.createTabEntry("global", globalTabWorkspaceRoot(), "")
			source := &SessionSourceRef{HostID: localDesktopHostID, Path: filepath.Join(t.TempDir(), "old.jsonl")}
			tab.HistoricalSource = source
			tab.SessionPath = source.Path
			tab.sink = &tabEventSink{tabID: tab.ID, app: app}
			app.publishRestoredTab(tab, release)
			if activate {
				if err := app.SetActiveTab(tab.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				app.startTabControllerBuild(tab)
			}
			app.mu.RLock()
			defer app.mu.RUnlock()
			if tab.buildGeneration != 0 || tab.Ctrl != nil || tab.HistoricalSource != source {
				t.Fatal("selecting a historical shell must not start a runtime before explicit preparation")
			}
		})
	}
}

func TestHistoricalDiscoveryIgnoresEmptyCanonicalDirectories(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	for _, id := range []string{"empty-read-probe", "damaged-history"} {
		if err := os.MkdirAll(filepath.Join(root, id), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "damaged-history", "manifest.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	app := newHistoricalLifecycleApp(t)
	status, err := app.ListHistoricalSessions()
	if err != nil || len(status.Items) != 1 || status.Items[0].Title != "damaged-history" {
		t.Fatalf("empty probes should be hidden but damaged artifacts preserved: %+v %v", status, err)
	}
}

func TestHistoricalRestoredSourceClearsOnCanonicalActivation(t *testing.T) {
	tab := &WorkspaceTab{HistoricalSource: &SessionSourceRef{HostID: localDesktopHostID, Path: "/fixture/old.jsonl"}}
	setTabSessionIdentity(tab, remoteSessionIDRoutePrefix+"canonical-target")
	if tab.HistoricalSource != nil {
		t.Fatal("pending source leaked into the next session")
	}
	if tab.SessionID != "canonical-target" {
		t.Fatal("test must activate a canonical identity")
	}
}
