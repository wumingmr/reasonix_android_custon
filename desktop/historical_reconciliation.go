package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/historywork"
	"reasonix/internal/session"
)

// Repair adoption before publishing ordinary rows. Discovery owns this work;
// sidebar pagination only reads its result and never starts a second migration.
func (a *App) reconcileHistoricalCatalog(ctx context.Context, rows []historicalCatalogEntry, sources map[string]historicalSource) error {
	for i := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := &rows[i]
		source := sources[entry.node.Source.SourceKey]
		release, err := a.historyMaintenance.BackgroundSlice(ctx, false)
		if err != nil {
			return err
		}
		err = a.reconcileHistoricalCatalogSource(ctx, entry.node.Source.SourceKey, source, entry.node.Health)
		release(historywork.ReadChunk)
		if err != nil && !historicalSourceBusyError(err) && !errors.Is(err, os.ErrPermission) && !errors.Is(err, context.Canceled) {
			// Unavailable rows remain discoverable for a later retry, but are not
			// advertised as usable conversations. No source bytes are removed.
			entry.node.Health = "unavailable"
			slog.Warn("desktop: historical source recovery deferred", "source_key", entry.node.Source.SourceKey)
		}
	}
	return ctx.Err()
}

func (a *App) reconcileHistoricalCatalogSource(ctx context.Context, key string, source historicalSource, health string) error {
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	mapping, mapped, resolveErr := state.ResolveSource(key)
	if mapped && a.historicalAdoptionReady(ctx, state, mapping, key) {
		return nil
	}
	if resolveErr != nil && !errors.Is(resolveErr, workspacestate.ErrMutationConflict) {
		return resolveErr
	}
	receipts, err := historicalReconciliationReceipts(ctx, source)
	if err != nil {
		return err
	}
	if !mapped && resolveErr == nil && len(receipts) == 0 && (health == "ok" || health == "metadata_pending") {
		return nil
	}
	releaseRuntime, ok := a.tryLockRuntimeMutation("reconcile historical source")
	if !ok {
		return errHistoricalSourceBusy
	}
	defer releaseRuntime()
	release, err := acquireHistoricalSource(ctx, key, source)
	if err != nil {
		return err
	}
	defer release()
	migration := desktopMigrationSource{scope: source.scope, workspaceRoot: source.root, headID: source.head, registeredSourceKey: key}
	cp, err := historicalReconciliationCheckpoint(source, migration)
	if err != nil {
		return err
	}
	fingerprint, err := desktopSourceFingerprint(source.path)
	if err != nil {
		return err
	}
	old, oldRef, finish, err := openHistoricalReconciliationSource(ctx, source)
	if err != nil {
		return err
	}
	defer finish()
	digest, err := canonicalMigrationDigest(ctx, old.Query(), oldRef)
	if err != nil {
		return err
	}
	if !mapped && resolveErr == nil && len(receipts) == 0 {
		return nil
	}
	target := mapping.SessionID
	if !mapped {
		target, err = a.selectCanonicalConversionTarget(ctx, migration, source.path, digest, receipts)
		if err != nil {
			return err
		}
		if target == "" {
			return workspacestate.ErrMutationConflict
		}
	}
	if mapped && fingerprint != mapping.Fingerprint {
		return fmt.Errorf("retained source changed: %w", workspacestate.ErrMutationConflict)
	}
	if err := cp.verify(); err != nil {
		return err
	}
	if err := a.repairMissingHistoricalTarget(ctx, old, oldRef, migration, source.path, target, fingerprint); err != nil {
		return fmt.Errorf("repair target: %w", err)
	}
	return a.recordHistoricalReconciliation(ctx, source, migration, cp, target, digest, fingerprint)
}

func (a *App) historicalAdoptionReady(ctx context.Context, state workspacestate.State, mapping workspacestate.SourceMapping, key string) bool {
	if state.SessionStates[mapping.SessionID].Lifecycle == workspacestate.Deleted {
		return true
	}
	if !containsDesktopString(state.Workspaces[mapping.WorkspaceID].SessionIDs, mapping.SessionID) || pendingHistoricalOperation(state, key) != nil {
		return false
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}
	info, err := a.desktopSessionService("").Query().Stat(ctx, ref)
	return err == nil && info.Error == "" && info.MetadataStatus != session.MetadataFailed
}

func (a *App) recordHistoricalReconciliation(ctx context.Context, source historicalSource, migration desktopMigrationSource, cp desktopMigrationCheckpoint, target, digest, fingerprint string) error {
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	workspace := desktopWorkspaceOwnerID(state, source.scope, source.root)
	if state.SessionStates[target].Lifecycle == workspacestate.Deleted || containsDesktopString(state.Workspaces[workspace].SessionIDs, target) {
		if actual, err := desktopSourceFingerprint(source.path); err != nil || actual != fingerprint {
			return errors.Join(err, workspacestate.ErrMutationConflict)
		}
		artifacts, err := retainedDesktopArtifacts(source.path)
		if err != nil {
			return err
		}
		if err := cp.verify(); err != nil {
			return err
		}
		if err := a.workspaceRegistry().RecordRecoveredSource(ctx, workspacestate.SourceMapping{SourceKey: migration.mappingKey(source.path), Path: source.path, Format: source.format,
			HeadID: source.head, SessionID: target, WorkspaceID: workspace, Fingerprint: fingerprint, RetainedArtifacts: artifacts}, state.Generation); err != nil {
			return err
		}
		return cp.complete(target, digest)
	}
	return a.completeRegisteredMigration(ctx, migration, cp, target, digest)
}

// Recover only an absent destination, using its original identity. Existing
// content (including a damaged or continued target) is never overwritten.
// An ordinary import journal makes publication restartable and lifecycle-fenced.
func (a *App) repairMissingHistoricalTarget(ctx context.Context, old *session.Service, oldRef session.SessionRef, source desktopMigrationSource, path, id, fingerprint string) error {
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	if state.SessionStates[id].Lifecycle == workspacestate.Deleted {
		return nil
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: id}
	query := a.desktopSessionService("").Query()
	if _, err := query.Snapshot(ctx, ref); err == nil {
		if op := pendingHistoricalOperation(state, source.mappingKey(path)); op != nil && strings.HasPrefix(op.ID, "repair-") && op.Mapping.SessionID == id {
			return a.finishHistoricalTargetRepair(ctx, *op)
		}
		return nil
	} else if !errors.Is(err, session.ErrSessionNotFound) {
		return err
	}
	if _, err := os.Lstat(filepath.Join(a.desktopSessions.root, id)); !os.IsNotExist(err) {
		return errors.Join(workspacestate.ErrMutationConflict, err)
	}
	return a.restoreHistoricalTargetBundle(ctx, old, oldRef, source, path, id, fingerprint, state)
}

func (a *App) restoreHistoricalTargetBundle(ctx context.Context, old *session.Service, ref session.SessionRef, source desktopMigrationSource, path, id, fingerprint string, state workspacestate.State) error {
	workspace, err := a.ensureDesktopMigrationWorkspace(ctx, source)
	if err != nil {
		return err
	}
	// This temporary export also validates every referenced blob before any
	// registry reservation or native target is published.
	tmp, err := os.MkdirTemp("", "reasonix-history-repair-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bundle := filepath.Join(tmp, "bundle")
	if err := old.TryExportCold(ctx, ref, bundle); err != nil {
		return err
	}
	a.lifecycleCheckpoint("historical-repair-source-exported")
	if actual, err := desktopSourceFingerprint(path); err != nil || actual != fingerprint {
		return errors.Join(err, workspacestate.ErrMutationConflict)
	}
	source.operationID = fmt.Sprintf("repair-%s-%d", source.mappingKey(path), state.Generation)
	if op := pendingHistoricalOperation(state, source.mappingKey(path)); op != nil && strings.HasPrefix(op.ID, "repair-") && op.Mapping.SessionID == id {
		source.operationID = op.ID
	}
	lifecycle := state.SessionStates[id].Lifecycle
	if lifecycle == "" {
		lifecycle = workspacestate.Active
	}
	presentation := state.Presentation[id]
	if err := a.workspaceRegistry().BeginOperation(ctx, workspacestate.Operation{ID: source.operationID, Presentation: &presentation,
		Kind: "import", WorkspaceID: workspace, SessionIDs: []string{id}, Lifecycle: lifecycle,
		ExpectedGeneration: state.Generation}); err != nil {
		return err
	}
	if _, err := a.prepareDesktopImport(ctx, source, path, fingerprint, id, workspace); err != nil {
		return fmt.Errorf("reserve repair: %w", err)
	}
	origin := session.SessionOriginCanonicalImport
	if info, err := os.Stat(path); err != nil {
		return err
	} else if !info.IsDir() {
		origin = session.SessionOriginLegacyImport
	}
	if _, err := a.desktopSessionService("").ImportWithHeader(ctx, bundle, session.CreateOptions{SessionID: id, CWD: desktopWorkspaceRoot(source.scope, source.workspaceRoot), Origin: origin}); err != nil {
		return err
	}
	a.lifecycleCheckpoint("historical-repair-content-published")
	current, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	return a.finishHistoricalTargetRepair(ctx, current.PendingOperations[source.operationID])
}

func (a *App) finishHistoricalTargetRepair(ctx context.Context, op workspacestate.Operation) error {
	if op.Mapping == nil || len(op.SessionIDs) != 1 || op.Mapping.SessionID != op.SessionIDs[0] {
		return workspacestate.ErrMutationConflict
	}
	path, fingerprint := op.Mapping.Path, op.Mapping.Fingerprint
	if actual, err := desktopSourceFingerprint(path); err != nil || actual != fingerprint {
		return errors.Join(err, workspacestate.ErrMutationConflict)
	}
	artifacts, err := retainedDesktopArtifacts(path)
	if err != nil {
		return err
	}
	mapping := *op.Mapping
	mapping.RetainedArtifacts = artifacts
	if err := a.workspaceRegistry().PrepareOperationContent(ctx, op.ID, op.SessionIDs, &mapping, op.Presentation); err != nil {
		return err
	}
	if err := a.workspaceRegistry().CommitOperation(ctx, op.ID); err != nil {
		return fmt.Errorf("commit repair: %w", err)
	}
	return nil
}
