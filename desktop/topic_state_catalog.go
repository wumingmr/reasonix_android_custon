package main

import (
	"strings"

	"reasonix/internal/sessioncatalog"
)

// ListProjectTopics surfaces topic-state failures instead of hiding them as empty pages.
func (a *App) ListProjectTopics(req ProjectTopicPageRequest) (ProjectTopicPage, error) {
	if catalog := a.sessionCatalog.Load(); catalog != nil {
		catalog.PrioritizeWorkspace(req.Scope, req.WorkspaceRoot)
	}
	// Remote roots come from Serve and must never map to local metadata paths.
	if strings.HasPrefix(strings.TrimSpace(req.WorkspaceRoot), "remote-project:") {
		return ProjectTopicPage{Items: []ProjectNode{}}, nil
	}
	if err := topicStateReadable(topicTitleRoot(req.Scope, req.WorkspaceRoot)); err != nil {
		return ProjectTopicPage{Items: []ProjectNode{}}, err
	}
	return a.unifiedProjectTopics(req)
}

func (a *App) GetTopicSummary(key ProjectTopicKey) (ProjectNode, error) {
	scope, workspaceRoot := normalizeDesktopTopicScope(key.Scope, key.WorkspaceRoot)
	topicID := strings.TrimSpace(key.TopicID)
	if topicID == "" {
		return ProjectNode{Children: []ProjectNode{}}, nil
	}
	allowMetadataFallback := true
	if catalog := a.sessionCatalog.Load(); catalog != nil {
		availability := a.catalogWorkspaceAvailability(catalog, scope, workspaceRoot)
		allowMetadataFallback = !availability.complete
		if !availability.usable {
			return a.metadataTopicSummary(scope, workspaceRoot, topicID), nil
		}
		ctx, cancel := a.catalogReadContext()
		defer cancel()
		topic, ok, err := catalog.GetTopic(ctx, sessioncatalog.TopicKey{
			Scope: scope, WorkspaceRoot: workspaceRoot, TopicID: topicID,
		})
		if err != nil {
			return ProjectNode{Children: []ProjectNode{}}, err
		}
		if ok {
			topicOverlays, sessionOverlays := a.catalogRuntimeOverlays()
			preferred, prefErr := catalog.PreferredOrdinarySessionPaths(ctx, scope, workspaceRoot)
			if prefErr != nil {
				preferred = nil
			}
			if node, visible := a.projectNodeFromCatalogTopic(topic, topicOverlays, sessionOverlays, preferred); visible {
				return node, nil
			}
		}
	}
	if !allowMetadataFallback {
		return ProjectNode{Children: []ProjectNode{}}, nil
	}
	return a.metadataTopicSummary(scope, workspaceRoot, topicID), nil
}

func (a *App) metadataTopicSummary(scope, workspaceRoot, topicID string) ProjectNode {
	for _, node := range a.metadataProjectTopics(scope, workspaceRoot) {
		if node.TopicID == topicID {
			return node
		}
	}
	return ProjectNode{Children: []ProjectNode{}}
}
