package workspacestate

import (
	"context"
	"encoding/json"
)

// Command receipts survive subsequent lifecycle changes. Retrying an old
// completed request must not execute it against the session's new state.
func (s *Store) BeginCommand(ctx context.Context, id, fingerprint string, request json.RawMessage, expected uint64) error {
	return s.beginCommand(ctx, id, fingerprint, request, expected, false)
}

// BeginPurgeCommand admits an observed snapshot without granting deletion
// authority. The purge transaction checks each target's generation under lock.
func (s *Store) BeginPurgeCommand(ctx context.Context, id, fingerprint string, request json.RawMessage, expected uint64) error {
	var command struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(request, &command); err != nil {
		return err
	}
	if command.Action != "purge" {
		return ErrMutationConflict
	}
	return s.beginCommand(ctx, id, fingerprint, request, expected, true)
}

func (s *Store) beginCommand(ctx context.Context, id, fingerprint string, request json.RawMessage, expected uint64, purge bool) error {
	return s.mutate(ctx, func(state *State) error {
		if old, exists := state.PendingOperations[id]; exists {
			if old.Kind != "command" || old.RequestFingerprint != fingerprint {
				return ErrMutationConflict
			}
			return nil
		}
		if expected > state.Generation || (!purge && expected != state.Generation) {
			return ErrMutationConflict
		}
		state.PendingOperations[id] = Operation{ID: id, Kind: "command", Phase: "prepared", Lifecycle: Active, SessionIDs: []string{}, ExpectedGeneration: expected, RequestFingerprint: fingerprint, Request: request}
		return nil
	})
}

func (s *Store) SaveCommandResult(ctx context.Context, id string, result json.RawMessage, complete bool) error {
	return s.mutate(ctx, func(state *State) error {
		op, exists := state.PendingOperations[id]
		if !exists || op.Kind != "command" {
			return ErrMutationConflict
		}
		if op.Phase == "committed" {
			return nil
		}
		// Stamp the receipt under the same writer lock as its commit. Other
		// processes may have advanced generation since the caller's snapshot.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(result, &fields); err != nil {
			return err
		}
		fields["generation"], _ = json.Marshal(state.Generation + 1)
		encoded, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		op.Result = encoded
		if complete {
			op.Phase = "committed"
		}
		op.ResultGeneration = state.Generation + 1
		state.PendingOperations[id] = op
		return nil
	})
}
