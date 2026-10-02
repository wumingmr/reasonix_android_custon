package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

var errPurgeSourceRetained = errors.New("migrated source must be retained")

// Cleanup is opt-in in the original tombstone transaction. Replaying an older
// purge must not grant new authority to remove upgrade evidence. Canonical
// directories use the same ownership lock and crash-safe staging as native
// deletion; shared content pools and legacy multi-head files remain untouched.
func (a *App) purgeMigratedSources(ctx context.Context, id string) error {
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	op := state.PendingOperations["purge-"+id]
	if len(op.Request) == 0 {
		return nil
	}
	var plan workspacestate.PurgeSourceCleanup
	if err := json.Unmarshal(op.Request, &plan); err != nil {
		return err
	}
	if plan.Version != 1 {
		return workspacestate.ErrUnsupportedVersion
	}
	for _, mapping := range plan.Sources {
		if mapping.SessionID != id || mapping.Format != "canonical" || mapping.HeadID != "" || !filepath.IsAbs(mapping.Path) {
			return workspacestate.ErrMutationConflict
		}
		// Never let a legacy receipt authorize deletion within the live store.
		if pathsOverlapForPurge(mapping.Path, a.desktopSessions.root) || !purgeSourceExclusive(state, id, mapping) {
			continue
		}
		// An offline or relocated historical root is not required to delete
		// the canonical conversation. The adoption tombstone still hides it
		// if it returns. Never follow a replaced directory symlink for cleanup.
		rootInfo, rootErr := os.Lstat(filepath.Dir(mapping.Path))
		if os.IsNotExist(rootErr) || (rootErr == nil && !rootInfo.IsDir()) {
			continue
		}
		if rootErr != nil {
			return rootErr
		}
		if info, err := os.Lstat(mapping.Path); err == nil && !info.IsDir() {
			continue
		}
		fs := session.NewFilesystemPersistence(filepath.Dir(mapping.Path))
		err := fs.PurgeWithTombstone(ctx, filepath.Base(mapping.Path), func() error {
			// Both directory ownership and writer ownership are held here.
			// Imports hold shared directory ownership through registry commit.
			current, err := a.workspaceRegistry().Load(ctx)
			if err != nil {
				return err
			}
			if !purgeSourceExclusive(current, id, mapping) {
				return errPurgeSourceRetained
			}
			fingerprint, err := desktopSourceFingerprint(mapping.Path)
			if err == nil && fingerprint != mapping.Fingerprint {
				return errPurgeSourceRetained
			}
			// An absent source can be a completed rename from a prior attempt.
			// FilesystemPersistence verifies ownership of any staged directory.
			if err != nil && !os.IsNotExist(err) {
				return errPurgeSourceRetained
			}
			a.lifecycleCheckpoint("before-source-cleanup")
			return nil
		})
		if err != nil && !errors.Is(err, errPurgeSourceRetained) {
			return err
		}
	}
	return nil
}

func purgeSourceExclusive(state workspacestate.State, id string, source workspacestate.SourceMapping) bool {
	if state.SessionStates[id].Lifecycle != workspacestate.Deleted || workspacestate.ClassifyPurge(state, id) != workspacestate.PurgeTombstoned {
		return false
	}
	registered, ok := state.SourceMappings[source.SourceKey]
	if !ok || registered.Path != source.Path || registered.SessionID != id || registered.Fingerprint != source.Fingerprint {
		return false
	}
	for _, mapping := range state.SourceMappings {
		if state.SessionStates[mapping.SessionID].Lifecycle != workspacestate.Deleted && purgeMappingReferences(mapping, source.Path) {
			return false
		}
	}
	for _, op := range state.PendingOperations {
		if op.Phase != "committed" && op.Mapping != nil && purgeMappingReferences(*op.Mapping, source.Path) {
			return false
		}
	}
	for _, recovery := range state.RecoveryEntries {
		if recovery.Status != "restored" && pathsOverlapForPurge(recovery.Path, source.Path) {
			return false
		}
	}
	return true
}

func purgeMappingReferences(mapping workspacestate.SourceMapping, source string) bool {
	if pathsOverlapForPurge(mapping.Path, source) {
		return true
	}
	for _, artifact := range mapping.RetainedArtifacts {
		if pathsOverlapForPurge(artifact, source) {
			return true
		}
	}
	return false
}

func pathsOverlapForPurge(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	l, errLeft := canonicalRuntimeRootErr(left)
	r, errRight := canonicalRuntimeRootErr(right)
	if errLeft != nil || errRight != nil {
		return true // Uncertain ownership is never deletion authority.
	}
	return l == r || strings.HasPrefix(l, r+string(filepath.Separator)) || strings.HasPrefix(r, l+string(filepath.Separator))
}
