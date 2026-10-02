package main

// workspaceEntryConversation selects an existing conversation without changing
// its identity or allocating a new one. Runtime selection remains in the normal
// activation path; legacy sources keep their exact path/head selector.
func (a *App) workspaceEntryConversation(root string) (string, string, error) {
	a.mu.RLock()
	for _, id := range a.orderedTabIDsLocked() {
		tab := a.tabs[id]
		if tabInWorkspace(tab, root) && topicTabReusableLocked(tab) {
			topicID, path := tab.TopicID, tab.SessionPath
			if tab.SessionID != "" {
				path = sessionRoute(tab.SessionID)
			}
			if topicID != "" || path != "" {
				a.mu.RUnlock()
				return topicID, path, nil
			}
		}
	}
	a.mu.RUnlock()
	page, err := a.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 1, SortMode: "updated", GroupFilter: "all"})
	if err != nil {
		return "", "", err
	}
	defer a.ReleaseReadSnapshot(page.SnapshotID)
	if len(page.Items) == 0 {
		return "", "", nil
	}
	node := page.Items[0]
	path := node.SessionPath
	if node.Session != nil {
		path = sessionRoute(node.Session.SessionID)
	} else if node.Source != nil {
		path = nativeSessionSourceRoute(node.Source)
	}
	return node.TopicID, path, nil
}
