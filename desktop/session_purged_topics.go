package main

import (
	"encoding/hex"
	"strings"

	"reasonix/desktop/internal/workspacestate"
)

func (a *App) validatePlaceholderTopicOpen(topicID, sessionPath string) error {
	if strings.TrimSpace(sessionPath) != "" || strings.TrimSpace(topicID) == "" {
		return nil
	}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return err
	}
	if purgedCanonicalTopicIDs(state)[topicID] {
		return newSessionOperationError(sessionOperationTargetNotFound, "This session was permanently deleted.")
	}
	return nil
}

// Deleted sessions still own their old metadata topics. A topic with another
// surviving session/reservation is shared and must remain available to it.
func purgedCanonicalTopicIDs(state workspacestate.State) map[string]bool {
	retired := map[string]bool{}
	for _, op := range state.PendingOperations {
		if (op.Kind != "purge" && op.Phase != "committed") || op.Presentation == nil || op.Presentation.TopicID == "" || len(op.SessionIDs) != 1 {
			continue
		}
		if state.SessionStates[op.SessionIDs[0]].Lifecycle == workspacestate.Deleted {
			retired[op.Presentation.TopicID] = true
		}
	}
	for id, status := range state.SessionStates {
		if status.Lifecycle != workspacestate.Deleted {
			continue
		}
		if topic := state.Presentation[id].TopicID; topic != "" {
			retired[topic] = true
		}
		// Previous manual-creation writers removed presentation on purge.
		// Their session and topic IDs share this exact persisted hash; this
		// recovers ownership without guessing from titles or deleting files.
		if suffix, ok := strings.CutPrefix(id, "desktop-manual-"); ok && len(suffix) == 32 {
			if _, err := hex.DecodeString(suffix); err == nil {
				retired["manual-"+suffix] = true
			}
		}
	}
	for id, presentation := range state.Presentation {
		if state.SessionStates[id].Lifecycle != workspacestate.Deleted {
			delete(retired, presentation.TopicID)
		}
	}
	for _, pending := range state.PendingCreates {
		if pending.Presentation != nil {
			delete(retired, pending.Presentation.TopicID)
		}
	}
	return retired
}
