package main

import (
	"os"
	"slices"

	"reasonix/desktop/internal/legacycleanup"
	"reasonix/desktop/internal/workspacestate"
)

// Old installations may have chat files but no indexed topic metadata. Their
// persisted source ownership is sufficient for archive, never for discard.
func (a *App) inspectLegacyTopicRemoval(state workspacestate.State, target TopicRemovalTarget) (TopicRemovalInspection, error) {
	out := TopicRemovalInspection{Target: target, Disposition: "archive_sessions", Allowed: true}
	owner := ""
	sources := map[string]any{}
	add := func(match topicSessionMatch) error {
		wid := topicRemovalWorkspaceID(state, match.scope, match.workspaceRoot)
		if (owner != "" && owner != wid) || (target.WorkspaceID != "" && target.WorkspaceID != wid) {
			return workspacestate.ErrMutationConflict
		}
		owner = wid
		info, err := os.Stat(match.path)
		if err != nil {
			return err
		}
		sources[match.path] = []any{match.scope, match.workspaceRoot, info.Size(), info.ModTime().UnixNano()}
		return nil
	}
	for _, dir := range a.knownSessionDirs() {
		index, err := topicSessionIndexForDir(dir)
		if err != nil {
			return out, err
		}
		for _, match := range index.byTopic[target.TopicID] {
			if err := add(match); err != nil {
				return out, err
			}
		}
	}
	a.mu.RLock()
	var runtimeSources []topicSessionMatch
	for _, tab := range a.runtimeTabsLocked() {
		if tab != nil && tab.TopicID == target.TopicID {
			if path := canonicalTabSessionPath(tab.currentSessionPath()); path != "" {
				runtimeSources = append(runtimeSources, topicSessionMatch{path: path, scope: tab.Scope, workspaceRoot: tab.WorkspaceRoot})
			}
		}
	}
	a.mu.RUnlock()
	for _, match := range runtimeSources {
		if _, found := sources[match.path]; !found {
			if err := add(match); err != nil {
				return out, err
			}
		}
	}
	if owner == "" {
		return out, workspacestate.ErrMutationConflict
	}
	out.Target.WorkspaceID = owner
	out.Token = topicRemovalToken(legacycleanup.Candidate{WorkspaceID: owner, TopicID: target.TopicID}, sources)
	if a.topicHasActiveRuntimeWork(target.TopicID) {
		out.Allowed, out.Reason = false, "busy"
	}
	return out, nil
}

func (a *App) validateCompatibleTopicOwner(state workspacestate.State, topicID string, hasSources bool) error {
	file, err := readTopicRemovalProjects()
	if err != nil {
		return err
	}
	owners := map[string]bool{}
	if slices.Contains(file.GlobalTopics, topicID) {
		owners[topicRemovalWorkspaceID(state, "global", "")] = true
	}
	for _, project := range file.Projects {
		if slices.Contains(project.Topics, topicID) {
			owners[topicRemovalWorkspaceID(state, "project", project.Root)] = true
		}
	}
	canonical, found, err := a.inspectCanonicalTopicRemoval(state, TopicRemovalTarget{TopicID: topicID})
	if err != nil {
		return err
	}
	if found {
		owners[canonical.Target.WorkspaceID] = true
	}
	if hasSources {
		legacy, err := a.inspectLegacyTopicRemoval(state, TopicRemovalTarget{TopicID: topicID})
		if err != nil {
			return err
		}
		owners[legacy.Target.WorkspaceID] = true
	}
	if len(owners) > 1 {
		return workspacestate.ErrMutationConflict
	}
	return nil
}
