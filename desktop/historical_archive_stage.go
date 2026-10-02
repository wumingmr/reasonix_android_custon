package main

import (
	"context"
	"fmt"
	"path/filepath"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
	"slices"
	"strings"
)

// The caller holds source ownership through the parent archive commit.
func (a *App) stageHistoricalArchive(ctx context.Context, id string, source historicalSource, fingerprint string) (workspacestate.SourceMapping, []string, error) {
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return workspacestate.SourceMapping{}, nil, err
	}
	migration, mapping, mapped, err := a.resolveHistoricalArchiveSource(ctx, state, id, source, fingerprint)
	if err != nil {
		return workspacestate.SourceMapping{}, nil, err
	}
	if !mapped {
		state, err = a.workspaceRegistry().Load(ctx)
		if err != nil {
			return workspacestate.SourceMapping{}, nil, err
		}
		op, err := a.claimHistoricalArchiveReservation(ctx, state, source, migration.mappingKey(source.path), fingerprint)
		if err != nil {
			return workspacestate.SourceMapping{}, nil, err
		}
		if op != nil {
			migration.operationID = op.ID
			if op.Phase == "content_ready" {
				ref := session.SessionRef{HostID: localDesktopHostID, SessionID: op.Mapping.SessionID}
				if err := a.validateDesktopWorkspaceMembership(ctx, op.WorkspaceID, ref); err != nil {
					return workspacestate.SourceMapping{}, nil, err
				}
				if _, err := a.desktopSessionService("").Query().Snapshot(ctx, ref); err != nil {
					return workspacestate.SourceMapping{}, nil, err
				}
				return *op.Mapping, []string{op.ID}, nil
			}
		}
		workspace, err := a.ensureDesktopWorkspace(ctx, source.scope, source.root)
		if err != nil {
			return workspacestate.SourceMapping{}, nil, err
		}
		if err := a.convertHistoricalSource(ctx, source, migration, workspace); err != nil {
			return workspacestate.SourceMapping{}, nil, fmt.Errorf("stage historical archive: %w", err)
		}
		state, err = a.workspaceRegistry().Load(ctx)
		if err != nil {
			return workspacestate.SourceMapping{}, nil, err
		}
		mapping, mapped, err = state.ResolveSource(migration.mappingKey(source.path))
		if err != nil {
			return workspacestate.SourceMapping{}, nil, err
		}
	}
	if mapped {
		return mapping, []string{}, nil
	}
	return historicalArchiveDependency(state, migration.mappingKey(source.path), fingerprint)
}

func historicalArchiveDependency(state workspacestate.State, key, fingerprint string) (workspacestate.SourceMapping, []string, error) {
	var mapping workspacestate.SourceMapping
	dependencies := []string{}
	{
		for _, op := range state.PendingOperations {
			if op.Kind != "archive-import" || op.Phase != "content_ready" || op.Mapping == nil ||
				op.Mapping.SourceKey != key || op.Mapping.Fingerprint != fingerprint {
				continue
			}
			if len(dependencies) != 0 || len(op.SessionIDs) != 1 || op.Mapping.SessionID != op.SessionIDs[0] {
				return workspacestate.SourceMapping{}, nil, workspacestate.ErrMutationConflict
			}
			mapping = *op.Mapping
			dependencies = append(dependencies, op.ID)
		}
		if len(dependencies) == 0 {
			return workspacestate.SourceMapping{}, nil, workspacestate.ErrMutationConflict
		}
	}
	return mapping, dependencies, nil
}

func (a *App) claimHistoricalArchiveReservation(ctx context.Context, state workspacestate.State, source historicalSource, key, fingerprint string) (*workspacestate.Operation, error) {
	var selected *workspacestate.Operation
	for _, op := range state.PendingOperations {
		if op.Phase == "committed" || op.Mapping == nil || op.Mapping.Fingerprint != fingerprint {
			continue
		}
		base := strings.TrimSuffix(key, ":review:"+fingerprint)
		if op.Mapping.SourceKey != key && (base == key || !slices.Contains(state.SourceKeys(op.Mapping.SourceKey), base)) {
			continue
		}
		if selected != nil || len(op.SessionIDs) != 1 {
			return nil, workspacestate.ErrMutationConflict
		}
		if op.Mapping.SessionID != op.SessionIDs[0] || op.Mapping.WorkspaceID != op.WorkspaceID {
			return nil, workspacestate.ErrMutationConflict
		}
		if !sameDesktopPath(op.Mapping.Path, source.path) || op.WorkspaceID != desktopWorkspaceOwnerID(state, source.scope, source.root) {
			return nil, workspacestate.ErrMutationConflict
		}
		copy := op
		selected = &copy
	}
	if selected == nil {
		return nil, nil
	}
	if selected.Kind == "import" {
		if selected.Phase == "content_ready" {
			release, err := session.NewFilesystemPersistence(a.desktopSessions.root).AcquireMaintenance(selected.SessionIDs[0])
			if err != nil {
				return nil, err
			}
			defer release()
			if err := a.validateHistoricalArchiveContent(ctx, source, *selected); err != nil {
				return nil, err
			}
		}
		if err := a.workspaceRegistry().StageHistoricalArchive(ctx, *selected, state.Generation, key); err != nil {
			return nil, err
		}
		selected.Kind, selected.Lifecycle = "archive-import", workspacestate.Archived
		mapping := *selected.Mapping
		mapping.SourceKey = key
		selected.Mapping = &mapping
	} else if selected.Kind != "archive-import" || selected.Mapping.SourceKey != key {
		return nil, workspacestate.ErrMutationConflict
	}
	return selected, nil
}

// Called under target maintenance ownership, including immediately before the
// parent commits. The journal fingerprint alone cannot prove published content.
func (a *App) validateHistoricalArchiveContent(ctx context.Context, source historicalSource, op workspacestate.Operation) error {
	if op.Mapping == nil || len(op.SessionIDs) != 1 {
		return workspacestate.ErrMutationConflict
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: op.SessionIDs[0]}
	if err := a.validateDesktopWorkspaceMembership(ctx, op.WorkspaceID, ref); err != nil {
		return err
	}
	got, err := canonicalMigrationDigest(ctx, a.desktopSessionService("").Query(), ref)
	if err != nil {
		return err
	}
	if source.format == "canonical" {
		old, err := session.NewService("archive-proof", session.NewFilesystemPersistence(filepath.Dir(source.path)))
		if err != nil {
			return err
		}
		defer shutdownHistoricalProofService(old)
		want, err := canonicalMigrationDigest(ctx, old.Query(), session.SessionRef{HostID: "archive-proof", SessionID: filepath.Base(source.path)})
		if err != nil {
			return err
		}
		if want != got {
			return workspacestate.ErrMutationConflict
		}
		return nil
	}
	key := desktopLegacyMigrationKey(source.path)
	if source.head != "" {
		key = desktopLegacyHeadKey(source.path, source.head)
	}
	if source.format == "canonical" {
		key = desktopCanonicalMigrationKey(filepath.Dir(source.path), filepath.Base(source.path))
	}
	ledger, err := readDesktopMigrationLedger()
	if err != nil {
		return err
	}
	record := ledger.Records[key]
	if strings.Contains(op.Mapping.SourceKey, ":review:") {
		if version, ok := ledger.Records[key+":review:"+op.Mapping.Fingerprint]; ok {
			record = version
		}
	}
	if record.TargetSessionID != ref.SessionID || record.ContentDigest == "" || got != record.ContentDigest {
		return workspacestate.ErrMutationConflict
	}
	return nil
}
