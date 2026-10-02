package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
	"reasonix/internal/store"
)

// Conversion receipts certify adoption of a particular history, not ownership
// of every future history written in the same directory. In particular, a
// tombstone must consume an identical copy without consuming later work.
func canonicalConversionReceipts(ctx context.Context, dir string) ([]desktopMigrationReceipt, error) {
	ledger, err := readDesktopMigrationLedger()
	if err != nil {
		return nil, err
	}
	manifest, err := readDesktopMigrationManifest(dir)
	if err != nil {
		return nil, err
	}
	var receipts []desktopMigrationReceipt
	add := func(record desktopMigrationRecord) {
		if record.Status == "completed" && record.TargetSessionID != "" && record.ContentDigest != "" {
			receipts = append(receipts, desktopMigrationReceipt{TargetSessionID: record.TargetSessionID, ContentDigest: record.ContentDigest})
		} else if record.PreviousCompletion != nil {
			receipts = append(receipts, *record.PreviousCompletion)
		}
	}
	baseKey := desktopCanonicalMigrationKey(filepath.Dir(dir), filepath.Base(dir))
	for key, record := range ledger.Records {
		if historicalSourceKeyMatches(key, baseKey) {
			add(record)
		}
	}
	if manifest.Source == nil {
		return receipts, nil
	}
	provenance := manifest.Source
	// A recorded conversion names this exact directory and its frozen head.
	// Do not match another conversion merely because both share an origin.
	for _, record := range ledger.Records {
		for _, conversion := range record.LegacyConversions {
			if sameDesktopPath(filepath.Join(conversion.Root, conversion.SessionID), dir) &&
				(provenance.LegacyHeadID == "" || conversion.HeadID == provenance.LegacyHeadID) {
				// The path record also inventories conversions of other heads;
				// its own adoption belongs only to the immutable primary head.
				if conversion.HeadID == record.LegacyPrimaryHead {
					add(record)
				} else if store.IsSessionTranscriptName(filepath.Base(provenance.Path)) {
					add(ledger.Records[desktopLegacyHeadKey(provenance.Path, conversion.HeadID)])
				}
			}
		}
	}
	if store.IsSessionTranscriptName(filepath.Base(provenance.Path)) && (provenance.Version == "" || provenance.Version == "legacy") {
		head := provenance.LegacyHeadID
		if head == "" {
			converted, resolveErr := resolveDesktopConversionHeads(ctx, provenance.Path, []desktopMigrationConversion{{LegacyDir: filepath.Join(dir, "legacy")}})
			if resolveErr != nil {
				if len(receipts) != 0 {
					return receipts, nil
				}
				return nil, resolveErr
			}
			head = converted[0].HeadID
		}
		if head != "" {
			add(ledger.Records[desktopLegacyHeadKey(provenance.Path, head)])
		}
		base := ledger.Records[desktopLegacyMigrationKey(provenance.Path)]
		if base.LegacyPrimaryHead == head {
			add(base)
		}
	} else {
		add(ledger.Records[desktopCanonicalMigrationKey(filepath.Dir(provenance.Path), filepath.Base(provenance.Path))])
	}
	return receipts, nil
}

func (a *App) reconcileCanonicalConversion(ctx context.Context, source desktopMigrationSource, cp desktopMigrationCheckpoint, digest string) (bool, error) {
	// Explicit "open a version" requests intentionally create a branch, even
	// when only metadata changed. Archive deduplication must not change that API.
	if source.versionFingerprint != "" && !source.deferArchive {
		return false, nil
	}
	path, _ := migrationCheckpointPath(cp)
	receipts, err := canonicalConversionReceipts(ctx, path)
	if err != nil {
		return true, errors.Join(newSessionOperationError("source_unavailable", "The historical source could not be verified. Its files were retained."), err)
	}
	target, err := a.selectCanonicalConversionTarget(ctx, source, path, digest, receipts)
	if err != nil {
		return true, err
	}
	if target == "" {
		return false, nil
	}
	if err := verifyCanonicalConversionClosure(ctx, path); err != nil {
		return true, errors.Join(newSessionOperationError("source_unavailable", "The historical source could not be verified. Its files were retained."), err)
	}
	return true, a.completeRegisteredMigration(ctx, source, cp, target, digest)
}

// Validate the full referenced content closure before consuming a residual
// directory. A transcript digest alone does not prove attachment blobs readable.
func verifyCanonicalConversionClosure(ctx context.Context, path string) error {
	tmp, err := os.MkdirTemp("", "reasonix-conversion-proof-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	service, err := session.NewService("conversion-proof", session.NewFilesystemPersistence(filepath.Dir(path)))
	if err != nil {
		return err
	}
	defer shutdownHistoricalProofService(service)
	return service.TryExportCold(ctx, session.SessionRef{HostID: "conversion-proof", SessionID: filepath.Base(path)}, filepath.Join(tmp, "verified"))
}

func shutdownHistoricalProofService(service *session.Service) {
	if err := service.Shutdown(context.Background()); err != nil {
		slog.Warn("desktop: historical proof service shutdown failed", "err", err)
	}
}

func (a *App) proveRetiredImportOrigin(ctx context.Context, state workspacestate.State, path, head, target string) error {
	if mapping, found, err := state.ResolveSource(desktopSourceKey(path, head)); err != nil {
		return err
	} else if found && mapping.SessionID == target {
		return nil
	}
	if info, err := os.Stat(path); err != nil {
		return err
	} else if info.IsDir() {
		receipts, err := canonicalConversionReceipts(ctx, path)
		if err != nil {
			return err
		}
		for _, receipt := range receipts {
			if receipt.TargetSessionID == target && receipt.ContentDigest != "" {
				return nil
			}
		}
	}
	return newSessionOperationError("source_ambiguous", "The historical source's relationship to a deleted session could not be verified.")
}

// Preserve the deletion barrier even for callers that use preparation directly.
func historicalRetiredError(lifecycle string) error {
	if lifecycle == workspacestate.Deleted {
		return session.ErrSessionNotFound
	}
	return errors.New("historical session is archived; restore it from the archive")
}
