package main

import (
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestRestoreRecoveryUsesSourceVersionOnDiskAfterFurtherChange(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionDir()
	sourceFile := filepath.Join(root, "old.jsonl")
	legacy := agent.NewSession("system")
	legacy.Add(provider.Message{ID: "old", Role: provider.RoleUser, Content: "original work"})
	if err := legacy.Save(sourceFile); err != nil {
		t.Fatal(err)
	}
	appendLine := func(content string) {
		legacy.Add(provider.Message{ID: content, Role: provider.RoleUser, Content: content})
		if err := legacy.Save(sourceFile); err != nil {
			t.Fatal(err)
		}
	}
	source := desktopMigrationSource{root: root, scope: "global", exact: map[string]bool{sourceFile: true}}
	migrate := func(app *App) {
		t.Helper()
		if err := app.migrateLegacyDirectory(t.Context(), source); err != nil {
			t.Fatal(err)
		}
		source.exact = nil
	}
	restart := func(app *App) *App {
		app.closeSessionServices()
		next := NewApp()
		t.Cleanup(next.closeSessionServices)
		return next
	}
	pending := func(app *App) []RecoveryEntryView {
		t.Helper()
		page, err := app.ListRecoveryEntries("", "", 50)
		if err != nil {
			t.Fatal(err)
		}
		return page.Items
	}

	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	migrate(app)
	appendLine("first-change")
	app = restart(app)
	migrate(app)
	entries := pending(app)
	if len(entries) != 1 || entries[0].Reason != "source_changed_after_adoption" {
		t.Fatalf("changed source was not quarantined: %+v", entries)
	}
	quarantined := entries[0].ID

	appendLine("second-change")
	app = restart(app)
	migrate(app)
	release, err := acquireHistoricalSource(t.Context(), desktopSourceKey(sourceFile, ""), historicalSource{path: sourceFile, format: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.RestoreRecoveryEntry(quarantined, ""); !historicalSourceBusyError(err) {
		t.Fatalf("restore did not refuse a source another writer holds: %v", err)
	}
	release()
	result, err := app.RestoreRecoveryEntry(quarantined, "")
	if err != nil {
		t.Fatalf("restore refused a source that changed again: %v", err)
	}
	history, err := app.desktopSessionService("").Query().History(t.Context(), session.SessionRef{HostID: localDesktopHostID, SessionID: result.Session.SessionID})
	if err != nil || len(history) == 0 || history[len(history)-1].Content != "second-change" {
		t.Fatalf("restore did not read the version on disk: %+v %v", history, err)
	}
	if left := pending(app); len(left) != 0 {
		t.Fatalf("restored version is still offered for recovery: %+v", left)
	}
	app = restart(app)
	for range 2 {
		migrate(app)
	}
	if left := pending(app); len(left) != 0 {
		t.Fatalf("restart re-quarantined the restored version: %+v", left)
	}
}
