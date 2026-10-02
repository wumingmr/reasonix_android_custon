package main

import "reasonix/internal/session"

// Resolve the durable identity before publishing a readable topic surface.
// Runtime ownership and controller startup belong to the existing tab build,
// not the synchronous phase of sidebar navigation.
func (a *App) canonicalTopicOpen(path string) (SessionTarget, *canonicalTabBinding, error) {
	id, ok := parseSessionRoute(path)
	if !ok {
		return SessionTarget{}, nil, nil
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: id}
	target, err := a.resolveCanonicalSessionTarget(ref, "")
	if err != nil {
		return SessionTarget{}, nil, err
	}
	workspace, err := a.canonicalSessionWorkspace(a.bootContext(), ref)
	if err != nil {
		return SessionTarget{}, nil, err
	}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return SessionTarget{}, nil, err
	}
	target.Scope, target.WorkspaceRoot = canonicalWorkspaceScope(workspace), workspace.Root
	if target.Scope == "global" {
		target.WorkspaceRoot = ""
	}
	title, source := a.canonicalTabTitle(a.bootContext(), state, ref)
	return target, &canonicalTabBinding{ref: ref, workspaceID: workspace.ID, title: title, titleSource: source}, nil
}

// A cancelled startup may still be unwinding when its session is selected
// again. Reusing that shell would wait on a build that can no longer publish.
// Leave it to its own completion and let the normal open path create a fresh
// surface for the same durable session. Callers hold App.mu.
func topicTabReusableLocked(tab *WorkspaceTab) bool {
	return tab != nil && !tab.removed && (tab.Ctrl != nil || tab.buildDoneGen == 0 || tab.buildDoneGen == tab.buildGeneration)
}

// newTopicTabLocked constructs the identity and profile before any runtime is
// started, so history readers can address the selected session immediately.
func (a *App) newTopicTabLocked(scope, workspaceRoot, actualRoot, topicID, sessionPath string, canonical *canonicalTabBinding) *WorkspaceTab {
	topicTitle, topicSource := "", ""
	if canonical != nil {
		topicTitle, topicSource = canonical.title, canonical.titleSource
	} else {
		topicTitle = topicTitleForTab(scope, workspaceRoot, topicID)
		if title, source, ok := topicTitleFallbackForOpen(workspaceRoot, topicID, sessionPath); ok {
			topicTitle = title
			_ = setTopicTitleWithSource(workspaceRoot, topicID, title, source)
		}
		topicSource = loadTopicTitleSource(topicTitleRoot(scope, workspaceRoot), topicID)
	}
	profile := defaultTabSessionProfile()
	if sessionPath != "" && canonical == nil {
		profile = loadTabSessionProfile(sessionPath)
	}
	tab := &WorkspaceTab{
		ID:               a.newUniqueTabIDLocked(),
		Scope:            scope,
		WorkspaceRoot:    actualRoot,
		TopicID:          topicID,
		TopicTitle:       topicTitle,
		topicTitleSource: topicSource,
		SessionPath:      sessionPath,
		disabledMCP:      map[string]ServerView{},
	}
	// Existing canonical sessions publish their identity now. New topics are
	// bound by the controller build without creating an empty legacy log.
	if canonical != nil {
		canonical.applyLocked(tab)
	}
	applyTabSessionProfile(tab, profile)
	tab.sink = &tabEventSink{tabID: tab.ID, app: a}
	return tab
}
