package main

import "reasonix/desktop/internal/workspacestate"

// A completed chat-file catalog cannot represent metadata-only placeholders.
// Keep those explicit indexed objects visible, but never resurrect a hidden
// recovery lineage or an adopted/archived formal session from its old metadata.
func (a *App) withRemovablePlaceholderTopics(req ProjectTopicPageRequest, state workspacestate.State, nodes []ProjectNode, adopted map[string]bool) []ProjectNode {
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		seen[node.TopicID] = true
	}
	file, err := readTopicRemovalProjects()
	if err != nil {
		return nodes
	}
	for _, node := range a.metadataProjectTopics(req.Scope, req.WorkspaceRoot) {
		if seen[node.TopicID] || adopted[node.TopicID] || node.RuntimeOnly {
			continue
		}
		if catalog := a.sessionCatalog.Load(); catalog != nil && catalog.TopicFolded(a.bootContext(), req.Scope, req.WorkspaceRoot, node.TopicID) {
			continue
		}
		item, err := topicRemovalCandidate(state, file, TopicRemovalTarget{TopicID: node.TopicID})
		if err != nil || item.Topic.Scope != req.Scope || (req.Scope == "project" && !sameDesktopPath(item.Topic.WorkspaceRoot, req.WorkspaceRoot)) {
			continue
		}
		// Partial discovery cannot prove emptiness. Listing must not scan sources
		// or transcripts for removability; InspectTopicRemoval owns that proof
		// when the user explicitly requests the management action.
		node.TurnsState = "unknown"
		node.Health = "metadata_pending"
		nodes = append(nodes, node)
		seen[node.TopicID] = true
	}
	return nodes
}
