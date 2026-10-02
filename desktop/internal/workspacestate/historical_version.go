package workspacestate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"reasonix/internal/fileutil"
)

// CommitHistoricalVersion adopts a validated, unregistered content_ready target
// as an explicitly requested version. The caller holds source/target ownership
// through validation and this commit. The original adoption remains immutable.
func (s *Store) CommitHistoricalVersion(ctx context.Context, observed Operation, versionKey string) error {
	return s.mutate(ctx, func(state *State) error {
		op, mapping, err := historicalVersionCandidate(state, observed, versionKey)
		if err != nil {
			return err
		}
		if err := validateHistoricalVersionDestination(state, op, mapping, versionKey); err != nil {
			return err
		}
		if historicalVersionReservationConflict(state, op, mapping, versionKey) {
			return ErrMutationConflict
		}
		if err := backupHistoricalVersionRegistry(s.path); err != nil {
			return err
		}
		mapping.SourceKey = versionKey
		op.Mapping = &mapping
		state.PendingOperations[op.ID] = op
		return commitOperation(state, op.ID, map[string]bool{})
	})
}

func historicalVersionCandidate(state *State, observed Operation, versionKey string) (Operation, SourceMapping, error) {
	op, exists := state.PendingOperations[observed.ID]
	if !exists || !reflect.DeepEqual(op, observed) || op.Kind != "import" || op.Phase != "content_ready" ||
		op.Lifecycle != Active || op.Mapping == nil || len(op.SessionIDs) != 1 || len(op.Dependencies) != 0 || op.RecoveryEntryID != "" {
		return Operation{}, SourceMapping{}, ErrMutationConflict
	}
	mapping := *op.Mapping
	if mapping.SourceKey == "" || mapping.Fingerprint == "" || versionKey != mapping.SourceKey+":review:"+mapping.Fingerprint ||
		mapping.SessionID != op.SessionIDs[0] || mapping.WorkspaceID != op.WorkspaceID {
		return Operation{}, SourceMapping{}, ErrMutationConflict
	}
	return op, mapping, nil
}

func validateHistoricalVersionDestination(state *State, op Operation, mapping SourceMapping, versionKey string) error {
	original, adopted := state.SourceMappings[mapping.SourceKey]
	if !adopted || original.SessionID == mapping.SessionID || original.Fingerprint == mapping.Fingerprint ||
		original.Path != mapping.Path || original.HeadID != mapping.HeadID || original.Format != mapping.Format ||
		original.WorkspaceID != op.WorkspaceID || state.SessionStates[original.SessionID].Lifecycle != Active {
		return ErrMutationConflict
	}
	owner, attached := sessionOwner(*state, original.SessionID)
	if !attached || owner != op.WorkspaceID {
		return ErrMutationConflict
	}
	if _, owned := sessionOwner(*state, mapping.SessionID); owned {
		return ErrMutationConflict
	}
	if _, known := state.SessionStates[mapping.SessionID]; known {
		return ErrMutationConflict
	}
	if _, creating := state.PendingCreates[mapping.SessionID]; creating {
		return ErrMutationConflict
	}
	if _, committed := state.SourceMappings[versionKey]; committed {
		return ErrMutationConflict
	}
	return nil
}

func historicalVersionReservationConflict(state *State, op Operation, mapping SourceMapping, versionKey string) bool {
	for id, other := range state.PendingOperations {
		if id == op.ID || other.Phase == "committed" {
			continue
		}
		if other.Mapping != nil && other.Mapping.SourceKey == versionKey {
			return true
		}
		if slices.Contains(other.SessionIDs, mapping.SessionID) {
			return true
		}
	}
	return false
}

func backupHistoricalVersionRegistry(path string) error {
	// Preserve the exact pre-repair registry, including unknown fields.
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	backup := filepath.Join(filepath.Dir(path), "historical-version-backups", fmt.Sprintf("%x.json", sha256.Sum256(body)))
	if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
		return err
	}
	return fileutil.AtomicWriteFileStrict(backup, body, 0600)
}
