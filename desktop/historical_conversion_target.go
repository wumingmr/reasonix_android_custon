package main

import (
	"context"
	"errors"
	"sort"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

// A receipt proves adoption of an exact source revision. The target may have
// advanced since then. Registry ownership takes precedence over old receipts;
// absent that, redundant retired receipts must not obscure a unique live owner.
func (a *App) selectCanonicalConversionTarget(ctx context.Context, source desktopMigrationSource, path, digest string, receipts []desktopMigrationReceipt) (string, error) {
	candidates := map[string]bool{}
	for _, receipt := range receipts {
		if receipt.ContentDigest == digest && receipt.TargetSessionID != "" {
			candidates[receipt.TargetSessionID] = true
		}
	}
	if len(candidates) == 0 {
		return "", nil
	}
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return "", err
	}
	if mapping, found, resolveErr := state.ResolveSource(source.mappingKey(path)); resolveErr == nil && found && candidates[mapping.SessionID] {
		return mapping.SessionID, nil
	}
	if len(candidates) == 1 {
		for id := range candidates {
			return id, nil
		}
	}
	live, retired := []string{}, []string{}
	for id := range candidates {
		if state.SessionStates[id].Lifecycle == workspacestate.Deleted {
			retired = append(retired, id)
			continue
		}
		if _, err := a.desktopSessionService("").Query().Stat(ctx, session.SessionRef{HostID: localDesktopHostID, SessionID: id}); err != nil {
			// A missing active destination still owns a recoverable identity.
			// Never pick another target merely because its files survived.
			return "", errors.Join(historicalOwnershipConflict(), err)
		}
		live = append(live, id)
	}
	if len(live) == 1 {
		return live[0], nil
	}
	if len(live) == 0 && len(retired) != 0 {
		// Every proved adoption was deleted. Keep the source retired rather
		// than importing it again; no active conversation is chosen here.
		sort.Strings(retired)
		return retired[0], nil
	}
	return "", historicalOwnershipConflict()
}

func historicalOwnershipConflict() error {
	return errors.Join(newSessionOperationError("source_ambiguous", "Conflicting migration receipts prevent identifying this historical source."), workspacestate.ErrMutationConflict)
}
