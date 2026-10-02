package main

import (
	"context"
	"log/slog"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/session"
)

type projectTreeRuntimeState struct {
	// activityAt records the last activity-status event per tab ID. The TTL
	// watchdog reaps a live status that sees no terminating event; the state
	// lives here (not on WorkspaceTab) to keep tabs.go within budget.
	activityMu sync.Mutex
	activityAt map[string]time.Time
}

// noteActivityStatus refreshes a tab's activity timestamp on every status
// event, even an unchanged one: the TTL watchdog measures silence, not how
// long a status has been displayed, so a long but active turn is never reaped.
func (s *projectTreeRuntimeState) noteActivityStatus(a *App, tabID string) {
	s.setActivityAt(tabID, time.Now())
	// Runtime snapshots, not silence, determine whether work remains active.
}

func (s *projectTreeRuntimeState) setActivityAt(tabID string, at time.Time) {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if s.activityAt == nil {
		s.activityAt = map[string]time.Time{}
	}
	s.activityAt[tabID] = at
}

func (s *projectTreeRuntimeState) activityAtSnapshot() map[string]time.Time {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	out := make(map[string]time.Time, len(s.activityAt))
	maps.Copy(out, s.activityAt)
	return out
}

func syncRuntimeWorkspaceRootSpelling(tab *WorkspaceTab, projects []desktopProject) bool {
	if tab == nil || tab.Scope != "project" {
		return false
	}
	i := projectIndexByRoot(projects, tab.WorkspaceRoot)
	if i < 0 || tab.WorkspaceRoot == projects[i].Root {
		return false
	}
	tab.WorkspaceRoot = projects[i].Root
	return true
}

// catalogRuntimeSnapshots copies runtime identity under App.mu, then lets all
// controller calls happen after the app lock is released. Controllers own
// their own locks and must never become part of the App.mu lock order.
func (a *App) catalogRuntimeSnapshots() []catalogRuntimeSnapshot {
	if a == nil {
		return []catalogRuntimeSnapshot{}
	}
	a.mu.RLock()
	snapshots := make([]catalogRuntimeSnapshot, 0, len(a.tabs)+len(a.detachedSessions))
	collect := func(tab *WorkspaceTab, open bool) {
		if tab == nil || strings.TrimSpace(tab.TopicID) == "" {
			return
		}
		snapshots = append(snapshots, catalogRuntimeSnapshot{
			tabID: tab.ID,
			scope: tab.Scope, workspaceRoot: tab.WorkspaceRoot, topicID: tab.TopicID,
			sessionPath: tab.SessionPath, sessionHeadID: tab.SessionHeadID, activity: tab.ActivityStatus, topicTitle: tab.TopicTitle,
			topicTitleSource: tab.topicTitleSource, ctrl: tab.Ctrl, open: open,
		})
	}
	for _, tab := range a.tabs {
		collect(tab, true)
	}
	for _, tab := range a.detachedSessions {
		collect(tab, false)
	}
	a.mu.RUnlock()
	return snapshots
}

// GetProjectTreeRuntimeSnapshot returns the complete in-memory runtime
// projection. The frontend subscribes first and then calls this method; the
// independent revision makes either arrival order deterministic.
func (a *App) GetProjectTreeRuntimeSnapshot() ProjectTreeRuntimeSnapshot {
	if a == nil {
		return ProjectTreeRuntimeSnapshot{Topics: []ProjectRuntimeTopic{}}
	}
	snapshot := a.GetRuntimeStateSnapshot()
	return ProjectTreeRuntimeSnapshot{Revision: snapshot.Revision, Topics: snapshot.Topics}
}

func cloneRuntimeTopics(topics []ProjectRuntimeTopic) []ProjectRuntimeTopic {
	var cloneNode func(ProjectNode) ProjectNode
	cloneNode = func(node ProjectNode) ProjectNode {
		next := node
		next.Children = make([]ProjectNode, len(node.Children))
		for i, child := range node.Children {
			next.Children[i] = cloneNode(child)
		}
		if node.Remote != nil {
			remote := *node.Remote
			next.Remote = &remote
		}
		return next
	}
	result := make([]ProjectRuntimeTopic, len(topics))
	for i, topic := range topics {
		result[i] = topic
		result[i].Node = cloneNode(topic.Node)
	}
	return result
}

func (a *App) projectTreeRuntimeTopics(snapshots []catalogRuntimeSnapshot) []ProjectRuntimeTopic {
	bySession := map[string]ProjectRuntimeTopic{}
	state, _ := a.workspaceRegistry().LoadProjection(a.bootContext())
	workspaceAliases := map[string]map[string][]string{}
	for _, snapshot := range snapshots {
		scope, root := normalizeDesktopTopicScope(snapshot.scope, snapshot.workspaceRoot)
		snapshot.scope, snapshot.workspaceRoot = scope, root
		if snapshot.sessionPath == "" && snapshot.ctrl != nil {
			snapshot.sessionPath = snapshot.ctrl.SessionPath()
		}
		nodes, _ := a.runtimeProjectTopicNodes(scope, root, []catalogRuntimeSnapshot{snapshot}, false)
		if len(nodes) == 0 {
			continue
		}
		node := nodes[0]
		node.TabID = snapshot.tabID
		path := strings.TrimSpace(snapshot.sessionPath)
		if id, ok := parseSessionRoute(path); ok {
			ref := session.SessionRef{HostID: localDesktopHostID, SessionID: id}
			node.Session, node.SessionPath, node.Key = &ref, sessionRoute(id), "canonical_"+id
		} else if identity, ok := snapshot.ctrl.(control.IdentityLifecycle); ok {
			if ref, bound := identity.SessionRef(); bound && ref.SessionID != "" {
				node.Session, node.SessionPath, node.Key = &ref, sessionRoute(ref.SessionID), "canonical_"+ref.SessionID
			}
		}
		if node.Session == nil && path != "" {
			node.SessionPath, node.Key = path, projectSessionNodeKey(scope, path)
			// A legacy transcript is listed by its source identity; publishing the
			// same one lets the renderer overlay this tab onto that row.
			node.Source = &SessionSourceRef{HostID: localDesktopHostID, Path: path, HeadID: snapshot.sessionHeadID, SourceKey: desktopSourceKey(path, snapshot.sessionHeadID)}
		}
		if node.Session != nil {
			workspaceID := desktopWorkspaceOwnerID(state, scope, root)
			aliases, exists := workspaceAliases[workspaceID]
			if !exists {
				aliases = workspaceSourceAliases(state, workspaceID)
				workspaceAliases[workspaceID] = aliases
			}
			node.IdentityAliases = append([]string{}, aliases[node.Session.SessionID]...)
			node.LifecycleGeneration = state.SessionStates[node.Session.SessionID].Generation
			if snapshot.tabID != "" {
				node.IdentityAliases = append(node.IdentityAliases, "tab\x00local\x00"+snapshot.tabID)
			}
		} else if path == "" && snapshot.tabID != "" {
			node.Key = "tab_" + snapshot.tabID
			// This tab was opened from an unbacked topic. Publish that exact
			// placeholder binding instead of asking the renderer to guess by title.
			node.IdentityAliases = []string{"topic\x00" + snapshot.topicID}
		}
		key := scope + "\x00" + root + "\x00" + node.Key
		bySession[key] = ProjectRuntimeTopic{Scope: scope, WorkspaceRoot: root, Node: node}
	}
	keys := make([]string, 0, len(bySession))
	for key := range bySession {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	topics := make([]ProjectRuntimeTopic, 0, len(keys))
	for _, key := range keys {
		topics = append(topics, bySession[key])
	}
	return topics
}

func (a *App) attachExistingSessionRuntime(tab *WorkspaceTab, path string, appCtx context.Context) bool {
	attached := a.attachExistingSessionRuntimeCore(tab, path, appCtx)
	if attached {
		a.emitProjectTreeRuntimeChangedWithLegacy()
	}
	return attached
}

func (a *App) closeTab(tabID string, allowDetach bool) error {
	err := a.closeTabRuntime(tabID, allowDetach)
	if err == nil {
		a.emitProjectTreeRuntimeChangedWithLegacy()
	}
	return err
}

func (a *App) emitProjectTreeChangedEvent() {
	if a.projectTreeChangedHook != nil {
		a.projectTreeChangedHook()
		return
	}
	a.emitProjectTreeRuntimeChanged()
	a.emitRuntimeEvent("project-tree:changed")
}

func (a *App) emitProjectTreeRuntimeChanged() {
	if a == nil {
		return
	}
	snapshot := a.GetRuntimeStateSnapshot()
	a.emitRuntimeProjection(snapshot, false)
}

func (a *App) emitRuntimeProjection(snapshot RuntimeStateProjection, legacy bool) {
	r := &a.runtimeStateProjection
	r.mu.Lock()
	if r.publishedEpoch == snapshot.Epoch && r.publishedRevision >= snapshot.Revision {
		r.mu.Unlock()
		return
	}
	r.publishedEpoch, r.publishedRevision = snapshot.Epoch, snapshot.Revision
	r.mu.Unlock()
	a.emitRuntimeEvent("project-tree:runtime-changed", ProjectTreeRuntimeSnapshot{Revision: snapshot.Revision, Topics: snapshot.Topics})
	a.emitRuntimeEvent("runtime-state:changed", snapshot)
	if legacy {
		a.emitRuntimeEvent("project-tree:changed", map[string]string{"reason": "runtime"})
	}
}

// The tagged legacy event keeps the previous frontend usable for one release.
func (a *App) emitProjectTreeRuntimeChangedWithLegacy() {
	a.emitProjectTreeRuntimeChanged()
	a.emitRuntimeEvent("project-tree:changed", map[string]string{"reason": "runtime"})
}

func (a *App) emitRuntimeEvent(name string, payload ...any) {
	if a != nil && a.ctx != nil {
		a.runtimeEvents.Emit(a.ctx, name, payload...)
	}
}

const (
	// topicActivityStatusTTL bounds how long a live spinner status may go
	// without any turn event before it is treated as orphaned. A flat TTL is
	// used instead of a per-session P99: turn durations are not tracked
	// per session in this package, and simplicity wins (#8528/#8555/#8859).
	topicActivityStatusTTL = 10 * time.Minute
)

// liveTopicActivityStatus reports statuses that must be terminated by a
// TurnDone event; if that event is lost the spinner would run forever.
// waiting_confirmation is excluded: it waits on the user, not on turn events.
func liveTopicActivityStatus(status string) bool {
	switch status {
	case topicStatusThinking, topicStatusStreaming:
		return true
	}
	return false
}

func (a *App) reapStaleTopicActivityStatus(now time.Time) {
	activityAt := a.projectTreeRuntime.activityAtSnapshot()
	a.mu.Lock()
	var reaped []string
	for _, tab := range a.runtimeTabsLocked() {
		if tab == nil || !liveTopicActivityStatus(tab.ActivityStatus) {
			continue
		}
		if at := activityAt[tab.ID]; at.IsZero() || now.Sub(at) < topicActivityStatusTTL {
			continue
		}
		reaped = append(reaped, tab.ID+":"+tab.ActivityStatus)
		tab.ActivityStatus = ""
	}
	a.mu.Unlock()
	if len(reaped) == 0 {
		return
	}
	slog.Warn("desktop: cleared stale topic activity status (no turn event within TTL)",
		"tabs", reaped, "ttl", topicActivityStatusTTL.String())
	a.emitProjectTreeRuntimeChangedWithLegacy()
}

// reconcileTabActivityStatus clears a live spinner status the session's
// controller does not corroborate — a TurnDone missed while the session was
// detached would otherwise spin forever after reopen. The controller query
// takes the controller's own lock, so it runs outside App.mu (the same lock
// order rule as catalogRuntimeSnapshots).
func (a *App) reconcileTabActivityStatus(tab *WorkspaceTab) bool {
	if a == nil || tab == nil {
		return false
	}
	a.mu.RLock()
	status := tab.ActivityStatus
	ctrl := tab.Ctrl
	a.mu.RUnlock()
	if ctrl == nil || !liveTopicActivityStatus(status) || ctrl.Running() {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if tab.ActivityStatus != status {
		// A new turn (or its TurnDone) landed while the locks were dropped.
		return false
	}
	tab.ActivityStatus = ""
	slog.Warn("desktop: cleared stale topic activity status on session open", "tab", tab.ID, "status", status)
	return true
}

// setTabActivityStatus records the project-tree status for a tab's in-flight
// turn and notes the event time for the TTL watchdog.
func (a *App) setTabActivityStatus(tabID, status string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	tab := a.tabByEventSinkIDLocked(tabID)
	if tab == nil {
		return false
	}
	a.projectTreeRuntime.noteActivityStatus(a, tab.ID)
	status = normalizeTopicStatus(status)
	if tab.ActivityStatus == status {
		return false
	}
	tab.ActivityStatus = status
	return true
}
