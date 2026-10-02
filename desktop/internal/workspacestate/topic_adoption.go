package workspacestate

import "maps"

// AdoptedTopicIDs includes durable adoption evidence after purge removes live
// membership and presentation. Display projections retain only this small
// derived index, not the operation journals or their request/content payloads.
func (s State) AdoptedTopicIDs(workspaceID string) map[string]bool {
	index := s.adoptedTopics
	if index == nil {
		index = adoptedTopicIndex(s)
	}
	return maps.Clone(index[workspaceID])
}

func adoptedTopicIndex(state State) map[string]map[string]bool {
	index := map[string]map[string]bool{}
	add := func(workspaceID, topicID string) {
		if workspaceID == "" || topicID == "" {
			return
		}
		if index[workspaceID] == nil {
			index[workspaceID] = map[string]bool{}
		}
		index[workspaceID][topicID] = true
	}
	for workspaceID, workspace := range state.Workspaces {
		for _, id := range workspace.SessionIDs {
			add(workspaceID, state.Presentation[id].TopicID)
		}
	}
	for _, op := range state.PendingOperations {
		if op.Phase != "committed" || op.Presentation == nil {
			continue
		}
		if op.Kind == "purge" && len(op.SessionIDs) == 1 && ClassifyPurge(state, op.SessionIDs[0]) == PurgeCommitted {
			add(op.WorkspaceID, op.Presentation.TopicID)
			continue
		}
		if op.Mapping == nil {
			continue
		}
		mapping, exists := state.SourceMappings[op.Mapping.SourceKey]
		if exists && mapping.WorkspaceID == op.WorkspaceID && mapping.SessionID == op.Mapping.SessionID {
			add(mapping.WorkspaceID, op.Presentation.TopicID)
		}
	}
	for _, removal := range state.TopicRemovals {
		if removal.Disposition == "archive_sessions" && removal.Phase == "committed" {
			add(removal.WorkspaceID, removal.TopicID)
		}
	}
	return index
}
