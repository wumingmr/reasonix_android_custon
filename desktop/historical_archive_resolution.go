package main

import (
	"context"
	"path/filepath"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

func (a *App) resolveHistoricalArchiveSource(ctx context.Context, state workspacestate.State, id string, source historicalSource, fingerprint string) (desktopMigrationSource, workspacestate.SourceMapping, bool, error) {
	mapping, mapped, err := state.ResolveSource(id)
	if err != nil {
		return desktopMigrationSource{}, workspacestate.SourceMapping{}, false, err
	}
	migration := desktopMigrationSource{scope: source.scope, workspaceRoot: source.root, headID: source.head, registeredSourceKey: id, deferArchive: true}
	changed := mapped && mapping.Fingerprint != fingerprint
	if !mapped && source.format == "canonical" {
		cp, err := newDesktopMigrationCheckpoint(migration, desktopCanonicalMigrationKey(filepath.Dir(source.path), filepath.Base(source.path)), canonicalMigrationSourceFiles(filepath.Dir(source.path), filepath.Base(source.path)))
		if err != nil {
			return desktopMigrationSource{}, workspacestate.SourceMapping{}, false, err
		}
		changed = cp.completed() && !cp.unchanged()
	}
	if changed {
		// A source selector addresses the retained file; canonical selectors
		// continue to address the adopted conversation independently of it.
		migration.versionFingerprint = fingerprint
		migration.registeredSourceKey = id + ":review:" + fingerprint
		mapped = false
	}
	if mapped && source.format == "canonical" {
		// The full fingerprint just matched the durable mapping. Refresh the
		// cheap discovery stamp after an identical file copy/metadata rewrite.
		cp, err := newDesktopMigrationCheckpoint(migration, desktopCanonicalMigrationKey(filepath.Dir(source.path), filepath.Base(source.path)), canonicalMigrationSourceFiles(filepath.Dir(source.path), filepath.Base(source.path)))
		if err != nil {
			return desktopMigrationSource{}, workspacestate.SourceMapping{}, false, err
		}
		if cp.completed() && cp.record.TargetSessionID == mapping.SessionID && !cp.unchanged() {
			if err := cp.complete(mapping.SessionID, cp.record.ContentDigest); err != nil {
				return desktopMigrationSource{}, workspacestate.SourceMapping{}, false, err
			}
		}
	}
	if changed && source.format == "canonical" && (state.SessionStates[mapping.SessionID].Lifecycle == workspacestate.Deleted || state.SessionStates[mapping.SessionID].Lifecycle == workspacestate.Archived) {
		if adopted, handled, err := a.reconcileRetiredArchiveVersion(ctx, source, migration); handled || err != nil {
			return migration, adopted, handled && err == nil, err
		}
	}
	return migration, mapping, mapped, nil
}

func (a *App) reconcileRetiredArchiveVersion(ctx context.Context, source historicalSource, migration desktopMigrationSource) (workspacestate.SourceMapping, bool, error) {
	root, id := filepath.Dir(source.path), filepath.Base(source.path)
	cp, err := newDesktopMigrationCheckpoint(migration, desktopCanonicalMigrationKey(root, id)+":review:"+migration.versionFingerprint, canonicalMigrationSourceFiles(root, id))
	if err != nil {
		return workspacestate.SourceMapping{}, true, err
	}
	old, err := session.NewService("archive-proof", session.NewFilesystemPersistence(root))
	if err != nil {
		return workspacestate.SourceMapping{}, true, err
	}
	defer shutdownHistoricalProofService(old)
	digest, err := canonicalMigrationDigest(ctx, old.Query(), session.SessionRef{HostID: "archive-proof", SessionID: id})
	if err != nil {
		return workspacestate.SourceMapping{}, true, err
	}
	if handled, err := a.reconcileCanonicalConversion(ctx, migration, cp, digest); !handled || err != nil {
		return workspacestate.SourceMapping{}, handled, err
	}
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return workspacestate.SourceMapping{}, true, err
	}
	mapping, found, err := state.ResolveSource(migration.mappingKey(source.path))
	if err == nil && !found {
		// The matching target may itself be active. Its source mapping was
		// staged for the parent archive rather than committed by reconciliation.
		return workspacestate.SourceMapping{}, false, nil
	}
	return mapping, true, err
}
