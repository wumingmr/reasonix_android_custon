package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"reasonix/internal/agent"
	"reasonix/internal/fileutil"
	"reasonix/internal/session"
)

func desktopLegacySourceFiles(path, pairedRoot string) []string {
	files := legacyMigrationSourceFiles(path)
	for _, root := range desktopLegacyStoreRoots(pairedRoot) {
		files = append(files, canonicalMigrationSourceFiles(root, agent.BranchID(path))...)
	}
	return files
}

// Before the identity cutover, SessionDirV3 / ProjectSessionDirV3 used this
// sibling directory. Its presence is independent of the current v4 root.
func desktopLegacyStoreRoots(root string) []string {
	if root == "" {
		return nil
	}
	parent := filepath.Dir(root)
	return []string{filepath.Join(parent, "sessions-v4"), filepath.Join(parent, "sessions-v3")}
}

func desktopLegacyPairedRoot(path, root string) (string, error) {
	for _, candidate := range desktopLegacyStoreRoots(root) {
		if _, err := os.Stat(filepath.Join(candidate, agent.BranchID(path))); err == nil {
			return candidate, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return root, nil
}

func desktopLegacyHeadKey(path, head string) string {
	digest := sha256.Sum256([]byte(canonicalRuntimeRoot(path) + "\x00head\x00" + head))
	return hex.EncodeToString(digest[:])
}

// The cached head list is certified by the same source revision as imports.
// Completed DAGs therefore need only stat calls on later startups. The first
// head keeps the historical path key even when the old app changes selection.
func (a *App) migrateLegacyHeads(ctx context.Context, path string, source desktopMigrationSource) error {
	key := desktopLegacyMigrationKey(path)
	files := desktopLegacyMigrationFiles(path, source)
	cp, err := newDesktopMigrationCheckpoint(source, key, files)
	if err != nil {
		return err
	}
	record := source.records[key]
	if len(record.LegacyHeads) == 0 || record.LegacyHeadsRevision != cp.revision {
		heads, err := session.LegacyMigrationHeads(ctx, path)
		if err != nil {
			return errors.Join(err, updateDesktopMigrationLedger(key, record.TargetSessionID, "failed", "source_heads"))
		}
		ids := []string{}
		selected := ""
		for _, head := range heads {
			if head.Retired {
				continue
			}
			ids = append(ids, head.ID)
			if head.Selected {
				selected = head.ID
			}
		}
		if len(heads) == 0 {
			ids = []string{""}
		}
		if len(ids) == 0 {
			return errors.New("legacy DAG has no live heads")
		}
		primary := selected
		if len(record.LegacyHeads) > 0 && slices.Contains(ids, record.LegacyHeads[0]) {
			primary = record.LegacyHeads[0]
		}
		if index := slices.Index(ids, primary); index > 0 {
			ids[0], ids[index] = ids[index], ids[0]
		}
		conversions, err := resolveDesktopConversionHeads(ctx, path, source.conversions[canonicalRuntimeRoot(path)])
		if err != nil {
			return errors.Join(err, updateDesktopMigrationLedger(key, record.TargetSessionID, "failed", "conversion_heads"))
		}
		if revision, err := desktopMigrationSourceRevision(files); err != nil || revision != cp.revision {
			return errors.Join(errors.New("legacy source changed while listing heads"), err,
				updateDesktopMigrationLedger(key, record.TargetSessionID, "failed", "source_changed"))
		}
		record, err = saveDesktopMigrationHeads(key, ids, selected, cp.revision, conversions)
		if err != nil {
			return err
		}
	}
	source.legacyAdoption = record.LegacyAdoption
	var joined error
	for _, head := range record.LegacyHeads {
		if err := ctx.Err(); err != nil {
			return errors.Join(joined, err)
		}
		headKey := key
		if head != record.LegacyPrimaryHead {
			headKey = desktopLegacyHeadKey(path, head)
		}
		perHead := source
		for _, converted := range record.LegacyConversions {
			if converted.HeadID != head {
				continue
			}
			perHead.headConversions = append(perHead.headConversions, converted)
			if source.handledStores != nil {
				source.handledStores[canonicalRuntimeRoot(filepath.Join(converted.Root, converted.SessionID))] = true
			}
		}
		if len(perHead.headConversions) > 0 && head == record.LegacySelectedHead && source.pairedRoot != "" {
			if err := perHead.includePairedConversion(path, head, headKey); err != nil {
				joined = errors.Join(joined, err)
				continue
			}
		}
		joined = errors.Join(joined, a.migrateLegacyHead(ctx, path, perHead, "", head, headKey, head == record.LegacySelectedHead))
	}
	return joined
}

// The identity cutover can leave a current paired store without a Source field
// alongside provenance-linked conversions. Include it before suppressing its scan.
func (source *desktopMigrationSource) includePairedConversion(path, head, key string) error {
	id := agent.BranchID(path)
	dir := filepath.Join(source.pairedRoot, id)
	manifest, err := readDesktopMigrationManifest(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.Join(err, updateDesktopMigrationLedger(key, "", "failed", "paired_read"))
	}
	if manifest.SessionID != id {
		return errors.Join(session.ErrDamagedStore, updateDesktopMigrationLedger(key, "", "failed", "paired_identity"))
	}
	for _, candidate := range source.headConversions {
		if sameDesktopPath(filepath.Join(candidate.Root, candidate.SessionID), dir) {
			return nil
		}
	}
	source.headConversions = append([]desktopMigrationConversion{{Root: source.pairedRoot, SessionID: id, HeadID: head, Codec: manifest.Codec}}, source.headConversions...)
	return nil
}

func saveDesktopMigrationHeads(key string, heads []string, selected, revision string, conversions []desktopMigrationConversion) (desktopMigrationRecord, error) {
	desktopMigrationMu.Lock()
	defer desktopMigrationMu.Unlock()
	release, lockErr := lockDesktopMigrationLedger()
	if lockErr != nil {
		return desktopMigrationRecord{}, lockErr
	}
	defer release()
	ledger, original, err := readDesktopMigrationLedgerFile()
	if err != nil {
		return desktopMigrationRecord{}, err
	}
	record := ledger.Records[key]
	if record.LegacyPrimaryHead == "" && len(heads) > 0 {
		record.LegacyPrimaryHead = heads[0]
	}
	if len(record.LegacyHeads) == 0 && record.LegacyAdoption == nil {
		if record.Status == "completed" {
			record.LegacyAdoption = &desktopMigrationReceipt{TargetSessionID: record.TargetSessionID, ContentDigest: record.ContentDigest, SourceRevision: record.SourceRevision}
		} else if record.PreviousCompletion != nil {
			receipt := *record.PreviousCompletion
			record.LegacyAdoption = &receipt
		}
	}
	record.SourceKey = key
	record.LegacyHeads, record.LegacySelectedHead, record.LegacyHeadsRevision = heads, selected, revision
	for i := range conversions {
		for _, previous := range record.LegacyConversions {
			if previous.Root == conversions[i].Root && previous.SessionID == conversions[i].SessionID && previous.HeadID == conversions[i].HeadID {
				conversions[i].extra = previous.extra
			}
		}
	}
	record.LegacyConversions = conversions
	ledger.Records[key] = record
	body, err := marshalDesktopMigrationRecord(original, ledger, key)
	if err != nil {
		return record, err
	}
	if err := os.MkdirAll(filepath.Dir(desktopMigrationLedgerPath()), 0o700); err != nil {
		return record, err
	}
	return record, fileutil.AtomicWriteFileStrict(desktopMigrationLedgerPath(), append(body, '\n'), 0o600)
}

func (a *App) migratePreviewSession(ctx context.Context, source desktopMigrationSource, id string) (retErr error) {
	key := desktopCanonicalMigrationKey(source.root, id)
	if source.versionFingerprint != "" {
		key += ":review:" + source.versionFingerprint
	}
	cp, err := newDesktopMigrationCheckpoint(source, key, canonicalMigrationSourceFiles(source.root, id))
	if err != nil {
		return err
	}
	if handled, err := a.checkAdoptedMigrationSource(ctx, source, cp); handled || err != nil {
		return err
	}
	if cp.unchanged() {
		return a.completeRegisteredMigration(ctx, source, cp, cp.record.TargetSessionID, cp.record.ContentDigest)
	}
	stage, ref, cleanup, err := stageDesktopStoredPreview(ctx, filepath.Join(source.root, id))
	if err != nil {
		return errors.Join(err, updateDesktopMigrationLedger(key, id, "failed", "preview_import"))
	}
	defer cleanup()
	return a.publishStagedMigration(ctx, source, cp, stage, ref, "", session.SessionOriginCanonicalImport)
}
