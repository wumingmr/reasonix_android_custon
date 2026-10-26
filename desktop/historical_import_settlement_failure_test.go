package main

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"reasonix/desktop/internal/workspacestate"
)

func TestHistoricalImportRetriesSettlementAfterReceiptCompletes(t *testing.T) {
	app, root, path := historicalRecoveryFixture(t)
	source := desktopMigrationSource{scope: "project", workspaceRoot: root}
	if err := app.migrateLegacySession(t.Context(), path, source, ""); err != nil {
		t.Fatal(err)
	}
	cp, err := newDesktopMigrationCheckpoint(source, desktopLegacyMigrationKey(path), desktopLegacyMigrationFiles(path, source))
	if err != nil || !cp.completed() {
		t.Fatalf("expected imported fixture receipt: %+v %v", cp, err)
	}
	if err := app.sourceRecovery(t.Context(), path, "legacy", "workspace_conflict", source.scope, root, source.headID); err != nil {
		t.Fatal(err)
	}
	before, err := app.workspaceRegistry().Load(t.Context())
	if err != nil || len(before.RecoveryEntries) != 1 {
		t.Fatalf("expected one pending recovery: %+v %v", before, err)
	}
	histories := v5MigrationHistories(t, app)
	if len(histories) != 1 {
		t.Fatalf("expected one readable imported history: %+v", histories)
	}
	// Recreate the boundary where the target and mapping are committed but the
	// receipt has not yet been published. No failed import is being retried.
	if err := os.Remove(desktopMigrationLedgerPath()); err != nil {
		t.Fatal(err)
	}
	assertSettlementFailsAfterReceipt(t, app, source, cp, path, before)
	for range 2 {
		if err := app.migrateLegacySession(t.Context(), path, source, ""); err != nil {
			t.Fatalf("completed-receipt replay failed: %v", err)
		}
		after, err := app.workspaceRegistry().Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for id, entry := range before.RecoveryEntries {
			if entry.Status != "pending" {
				t.Fatalf("fixture was not pending: %+v", entry)
			}
			entry.Status, entry.SessionID = "restored", cp.record.TargetSessionID
			if !reflect.DeepEqual(after.RecoveryEntries[id], entry) {
				t.Fatalf("replay did not settle exact recovery: %+v", after.RecoveryEntries)
			}
		}
		if !reflect.DeepEqual(after.SourceMappings, before.SourceMappings) ||
			!reflect.DeepEqual(after.Workspaces, before.Workspaces) ||
			!reflect.DeepEqual(after.SessionStates, before.SessionStates) ||
			!reflect.DeepEqual(v5MigrationHistories(t, app), histories) {
			t.Fatal("settlement replay changed adoption, lifecycle, membership or readable history")
		}
	}
}

func assertSettlementFailsAfterReceipt(t *testing.T, app *App, source desktopMigrationSource, cp desktopMigrationCheckpoint, path string, before workspacestate.State) {
	t.Helper()
	registryPath := app.workspaceRegistry().Path()
	body, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	// A malformed registry injects a deterministic real settlement read failure,
	// without timing, permissions (tests may run as root), or production hooks.
	if err := os.WriteFile(registryPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(registryPath, body, 0600) })
	fingerprint, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	err = app.completeMigrationRecovery(t.Context(), source, cp, path, fingerprint, cp.record.TargetSessionID, cp.record.ContentDigest)
	var syntaxError *json.SyntaxError
	if !errors.As(err, &syntaxError) {
		t.Fatalf("expected registry settlement error after receipt completion, got %v", err)
	}
	ledger, err := readDesktopMigrationLedger()
	if err != nil {
		t.Fatal(err)
	}
	receipt := ledger.Records[cp.key]
	if receipt.Status != "completed" || receipt.TargetSessionID != cp.record.TargetSessionID ||
		receipt.ContentDigest != cp.record.ContentDigest || receipt.SourceRevision != cp.revision {
		t.Fatalf("receipt was not durably completed before settlement failed: %+v", receipt)
	}
	if got, err := os.ReadFile(registryPath); err != nil || string(got) != "{" {
		t.Fatalf("failed settlement overwrote unavailable registry: %q %v", got, err)
	}
	if err := os.WriteFile(registryPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	after, err := app.workspaceRegistry().Load(t.Context())
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("failed settlement changed recovery or adoption: %+v %v", after, err)
	}
}
