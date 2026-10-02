package main

import (
	"context"
	"errors"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

func validateDesktopOperationSources(state workspacestate.State, op workspacestate.Operation) error {
	checks := []workspacestate.Operation{op}
	for _, dependency := range op.Dependencies {
		checks = append(checks, state.PendingOperations[dependency])
	}
	for _, check := range checks {
		if check.Mapping == nil {
			continue
		}
		// Published import content is validated through the target snapshot and
		// workspace guards. The mapping retains evidence of its original source.
		if (check.Kind == "import" || check.Kind == "restore") && check.Phase == "content_ready" {
			continue
		}
		fingerprint, err := desktopSourceFingerprint(check.Mapping.Path)
		if err != nil || fingerprint != check.Mapping.Fingerprint {
			return errors.Join(err, workspacestate.ErrMutationConflict)
		}
	}
	return nil
}

func pendingHistoricalOperation(state workspacestate.State, id string) *workspacestate.Operation {
	var resume *workspacestate.Operation
	for _, candidate := range state.PendingOperations {
		if candidate.Phase == "committed" || candidate.Mapping == nil {
			continue
		}
		matches := false
		for _, key := range state.SourceKeys(candidate.Mapping.SourceKey) {
			matches = matches || historicalSourceKeyMatches(key, id)
		}
		if !matches {
			continue
		}
		if candidate.Kind != "import" && candidate.Kind != "restore" {
			continue
		}
		if resume == nil || historicalOperationRank(candidate) < historicalOperationRank(*resume) ||
			(historicalOperationRank(candidate) == historicalOperationRank(*resume) && candidate.ID < resume.ID) {
			copy := candidate
			resume = &copy
		}
	}
	return resume
}

// Committed mappings and published content are independent of retained sources.
// Check them before touching a source lock, path, fingerprint, or version.
func (a *App) resumeReadyHistoricalImport(ctx context.Context, state workspacestate.State, id string, source historicalSource) (SessionRestoreResult, bool, error) {
	mapping, mapped, err := historicalMappingForSource(state, id)
	if source.version != "" {
		mapping, mapped, err = state.ResolveSource(id)
	}
	if err != nil {
		return SessionRestoreResult{}, true, err
	}
	if mapped {
		if state.SessionStates[mapping.SessionID].Lifecycle != workspacestate.Active {
			return SessionRestoreResult{}, true, errors.New("historical session was archived or deleted; use the archive to restore it")
		}
		ref := session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}
		if _, err := a.desktopSessionService("").Query().Stat(ctx, ref); err != nil {
			return SessionRestoreResult{}, true, err
		}
		return SessionRestoreResult{Session: ref, WorkspaceID: mapping.WorkspaceID, Generation: state.Generation}, true, nil
	}
	for _, op := range state.PendingOperations {
		if op.Kind != "archive-import" || op.Phase == "committed" || op.Mapping == nil {
			continue
		}
		for _, key := range state.SourceKeys(op.Mapping.SourceKey) {
			if historicalSourceKeyMatches(key, id) {
				return SessionRestoreResult{}, true, errHistoricalSourceBusy
			}
		}
	}
	op := pendingHistoricalOperation(state, id)
	if op == nil || op.Phase != "content_ready" || op.Lifecycle != workspacestate.Active {
		return SessionRestoreResult{}, false, nil
	}
	if err := a.replayDesktopSessionOperation(ctx, state, *op); err != nil {
		return SessionRestoreResult{}, true, err
	}
	updated, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return SessionRestoreResult{}, true, err
	}
	committed := updated.PendingOperations[op.ID]
	if committed.Phase != "committed" || len(committed.SessionIDs) != 1 {
		return SessionRestoreResult{}, true, workspacestate.ErrMutationConflict
	}
	return SessionRestoreResult{Session: session.SessionRef{HostID: localDesktopHostID, SessionID: committed.SessionIDs[0]}, WorkspaceID: committed.WorkspaceID, Generation: committed.ResultGeneration}, true, nil
}
