package main

import (
	"context"
	"slices"

	"reasonix/desktop/internal/workspacestate"
)

// Recovery takes source guards in the same order as initial archive staging,
// and retains them until the parent commit has finished.
func freezeArchiveDependencies(ctx context.Context, state workspacestate.State, op workspacestate.Operation) (func(), error) {
	releases := []func(){}
	release := func() {
		for _, done := range slices.Backward(releases) {
			done()
		}
	}
	seen := map[string]bool{}
	for _, id := range op.Dependencies {
		child := state.PendingOperations[id]
		if child.Kind != "archive-import" || child.Mapping == nil {
			release()
			return nil, workspacestate.ErrMutationConflict
		}
		mapping := child.Mapping
		key := desktopSourceKey(mapping.Path, mapping.HeadID)
		if seen[key] {
			continue
		}
		seen[key] = true
		done, err := acquireHistoricalSource(ctx, key, historicalSource{path: mapping.Path, format: mapping.Format, head: mapping.HeadID})
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, done)
	}
	return release, nil
}

func (a *App) validateRecoveredHistoricalArchive(ctx context.Context, state workspacestate.State, op workspacestate.Operation) error {
	// Older topic archives used their existing snapshot/fingerprint contract.
	// This deterministic parent ID identifies the stronger source-archive flow.
	if len(op.Dependencies) != 1 || op.ID != "archive-"+op.Dependencies[0] {
		return nil
	}
	child := state.PendingOperations[op.Dependencies[0]]
	if child.Mapping == nil {
		return workspacestate.ErrMutationConflict
	}
	mapping := child.Mapping
	return a.validateHistoricalArchiveContent(ctx, historicalSource{path: mapping.Path, format: mapping.Format, head: mapping.HeadID}, child)
}
