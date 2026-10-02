package workspacestate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// TopicSessionRemovalToken is shared by the inspection and durable commit
// owners. Rechecking only before staging leaves a cross-process rename gap.
func TopicSessionRemovalToken(state State, workspaceID, topicID string) (string, string, error) {
	associations := map[string]any{}
	owner := ""
	for id, status := range state.SessionStates {
		presentation := state.Presentation[id]
		if (presentation.TopicID != topicID && "canonical-"+id != topicID) || status.Lifecycle == Deleted {
			continue
		}
		owners := 0
		for wid, workspace := range state.Workspaces {
			if !slices.Contains(workspace.SessionIDs, id) {
				continue
			}
			owners++
			if (owner != "" && owner != wid) || (workspaceID != "" && workspaceID != wid) {
				return "", "", ErrMutationConflict
			}
			owner = wid
		}
		if owners != 1 {
			return "", "", ErrMutationConflict
		}
		associations[id] = []any{presentation, status}
	}
	if len(associations) == 0 {
		return "", "", nil
	}
	body, err := json.Marshal([]any{owner, topicID, associations, state.Workspaces[owner].Organization})
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), owner, nil
}

// Runs under the same cross-process transaction as lifecycle publication,
// including startup replay. A committed child is an idempotent receipt.
func validateTopicRemovalArchive(state State, op Operation) error {
	id, topicArchive := strings.CutPrefix(op.ID, "topic-sessions-")
	if !topicArchive || op.Kind != "archive" || op.Phase == "committed" {
		return nil
	}
	removal, exists := state.TopicRemovals[id]
	if !exists {
		return nil
	}
	token, _, err := TopicSessionRemovalToken(state, removal.WorkspaceID, removal.TopicID)
	if err != nil {
		return err
	}
	if removal.SessionToken == "" {
		// A legacy-only intent cannot silently acquire a new formal identity.
		if token != "" {
			return ErrMutationConflict
		}
		return nil
	}
	if token != removal.SessionToken {
		return ErrMutationConflict
	}
	return nil
}
