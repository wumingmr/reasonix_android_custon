package workspacestate

import "context"

// restoredRecoveryVersion reports the session a source version was restored
// into. Entries are keyed by version, so the version, not whichever entry first
// reported it, decides whether recovery is still owed.
func restoredRecoveryVersion(state State, sourceKey, fingerprint string) (string, bool) {
	if fingerprint == "" {
		return "", false
	}
	found, sessionID := "", ""
	for id, entry := range state.RecoveryEntries {
		if entry.Status == "restored" && entry.SessionID != "" && entry.SourceKey == sourceKey &&
			entry.Fingerprint == fingerprint && (found == "" || id < found) {
			found, sessionID = id, entry.SessionID
		}
	}
	return sessionID, found != ""
}

func settleRecoveryVersion(state *State, restored RecoveryEntry) {
	if restored.Fingerprint == "" {
		return
	}
	for id, entry := range state.RecoveryEntries {
		if entry.Status != "restored" && entry.SourceKey == restored.SourceKey && entry.Fingerprint == restored.Fingerprint {
			entry.Status, entry.SessionID = "restored", restored.SessionID
			state.RecoveryEntries[id] = entry
		}
	}
}

// RebaseRecovery points a pending entry at the source version now on disk; the
// caller holds the source lock until its restore commits. A version already
// restored settles the entry to that session instead of importing it again.
func (s *Store) RebaseRecovery(ctx context.Context, id, fingerprint string) (RecoveryEntry, error) {
	var rebased RecoveryEntry
	err := s.mutate(ctx, func(state *State) error {
		entry, ok := state.RecoveryEntries[id]
		if !ok {
			return ErrMutationConflict
		}
		if entry.Status != "restored" {
			entry.Fingerprint = fingerprint
			if sessionID, restored := restoredRecoveryVersion(*state, entry.SourceKey, fingerprint); restored {
				entry.Status, entry.SessionID = "restored", sessionID
			}
			state.RecoveryEntries[id] = entry
		}
		rebased = entry
		return nil
	})
	return rebased, err
}
