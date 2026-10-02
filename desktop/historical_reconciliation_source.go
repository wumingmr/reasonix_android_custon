package main

import (
	"context"
	"os"
	"path/filepath"

	"reasonix/internal/session"
)

func historicalReconciliationCheckpoint(source historicalSource, migration desktopMigrationSource) (desktopMigrationCheckpoint, error) {
	if source.format == "canonical" {
		return newDesktopMigrationCheckpoint(migration, desktopCanonicalMigrationKey(filepath.Dir(source.path), filepath.Base(source.path)), canonicalMigrationSourceFiles(filepath.Dir(source.path), filepath.Base(source.path)))
	}
	key := desktopLegacyMigrationKey(source.path)
	if source.head != "" {
		key = desktopLegacyHeadKey(source.path, source.head)
	}
	return newDesktopMigrationCheckpoint(migration, key, desktopLegacyMigrationFiles(source.path, migration))
}

func historicalReconciliationReceipts(ctx context.Context, source historicalSource) ([]desktopMigrationReceipt, error) {
	if source.format == "canonical" {
		return canonicalConversionReceipts(ctx, source.path)
	}
	ledger, err := readDesktopMigrationLedger()
	if err != nil {
		return nil, err
	}
	key := desktopLegacyMigrationKey(source.path)
	if source.head != "" {
		key = desktopLegacyHeadKey(source.path, source.head)
	}
	receipts := []desktopMigrationReceipt{}
	for id, record := range ledger.Records {
		if !historicalSourceKeyMatches(id, key) {
			continue
		}
		if record.Status == "completed" && record.TargetSessionID != "" && record.ContentDigest != "" {
			receipts = append(receipts, desktopMigrationReceipt{TargetSessionID: record.TargetSessionID, ContentDigest: record.ContentDigest})
		} else if record.PreviousCompletion != nil {
			receipts = append(receipts, *record.PreviousCompletion)
		}
	}
	return receipts, nil
}

// Legacy normalization happens in disposable storage. Source files, selected
// DAG heads and old-format metadata are never rewritten during recovery.
func openHistoricalReconciliationSource(ctx context.Context, source historicalSource) (*session.Service, session.SessionRef, func(), error) {
	root := filepath.Dir(source.path)
	cleanup := func() {}
	if source.format != "canonical" {
		var err error
		root, err = os.MkdirTemp("", "reasonix-history-proof-")
		if err != nil {
			return nil, session.SessionRef{}, cleanup, err
		}
		cleanup = func() { _ = os.RemoveAll(root) }
	}
	old, err := session.NewService("migration-source", session.NewFilesystemPersistence(root))
	if err != nil {
		cleanup()
		return nil, session.SessionRef{}, func() {}, err
	}
	finish := func() { shutdownHistoricalProofService(old); cleanup() }
	ref := session.SessionRef{HostID: "migration-source", SessionID: filepath.Base(source.path)}
	if source.format != "canonical" {
		runtime, _, err := old.ContinueImported(ctx, source.path, source.head)
		if err != nil {
			finish()
			return nil, ref, func() {}, err
		}
		ref = runtime.Ref()
		if err := old.Close(ctx, ref); err != nil {
			finish()
			return nil, ref, func() {}, err
		}
	}
	return old, ref, finish, nil
}
