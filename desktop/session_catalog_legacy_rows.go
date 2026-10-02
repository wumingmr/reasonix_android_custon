package main

import (
	"reasonix/internal/agent"
	"reasonix/internal/sessioncatalog"
	"sort"
	"strings"
)

// projectNodesFromCatalogTopic expands every ordinary, independently
// addressable legacy session into its own sidebar row. TopicID remains shared
// history metadata; SessionPath is the operation and navigation target.
// Recovery-only topics intentionally keep their single summary row because
// their physical replicas are managed by the saved-versions surface.
func (a *App) projectNodesFromCatalogTopic(topic sessioncatalog.TopicRecord, topicOverlays, sessionOverlays map[string]catalogRuntimeOverlay, preferred map[string]struct{}) []ProjectNode {
	base, visibleTopic := a.projectNodeFromCatalogTopic(topic, topicOverlays, sessionOverlays, preferred)
	if !visibleTopic {
		return []ProjectNode{}
	}
	if topic.RecoveryState == "recovery_only" {
		return []ProjectNode{base}
	}
	localPreferred := preferred
	if localPreferred == nil {
		localPreferred = sessioncatalog.PreferredOrdinarySessionPaths(topic.Sessions)
	}
	kind := "topic"
	if topic.Scope == "global" {
		kind = "global_topic"
	}
	nodes := make([]ProjectNode, 0, len(topic.Sessions))
	projects := loadProjectsFile()
	coveredRuntime := coveredLegacyRuntime(topic.Sessions, sessionOverlays, localPreferred)
	for _, record := range topic.Sessions {
		overlay := sessionOverlays[sessionRuntimeKey(record.Path)]
		// Covered recovery copies remain represented by their logical parent in
		// the ordinary tree, even while an older restored tab still has the copy
		// open. The dedicated recovery surface owns the physical copy.
		if hiddenLegacyRecovery(record, localPreferred) {
			continue
		}
		if !sessioncatalog.OrdinaryTreeSession(record, overlay.open, overlay.running, localPreferred) {
			continue
		}
		if inherited := coveredRuntime[agent.BranchID(record.Path)]; inherited.open || inherited.running {
			overlay.open = overlay.open || inherited.open
			overlay.running = overlay.running || inherited.running
			if overlay.status == "" {
				overlay.status = inherited.status
			}
		}
		label := strings.TrimSpace(record.CustomTitle)
		if label == "" {
			label = a.localizedTopicTitle(topic.Title, topic.TitleSource)
		}
		turnsState := string(record.TurnsState)
		if turnsState == "" {
			turnsState = base.TurnsState
		}
		health := string(record.Health)
		if health == "" {
			health = base.Health
		}
		node := ProjectNode{
			Key: projectSessionNodeKey(topic.Scope, record.Path), Kind: kind, Label: label,
			Root: topic.WorkspaceRoot, TopicID: topic.TopicID, SessionPath: record.Path,
			Preview: strings.TrimSpace(record.Preview), Turns: record.Turns,
			TurnsState: turnsState, Health: health,
			CreatedAt: record.CreatedAt, LastActivityAt: record.LastActivityAt,
			Pinned: topic.Pinned, SortOrder: topic.SortOrder,
			Recovered: record.Recovered, RecoveryReason: record.RecoveryReason,
			RecoveryDigest: record.RecoveryDigest, RecoveryParentID: record.ParentID,
			Open: overlay.open, Running: overlay.running, Status: overlay.status,
			Children: []ProjectNode{},
		}
		if rank := sessionOrderRank(projects, topic.Scope, topic.WorkspaceRoot, projectNodeSessionKey(node)); rank >= 0 {
			node.SortOrder = rank
		} else if (topic.Scope == "global" && projects.GlobalManualSessionOrder) || (topic.Scope == "project" && projectManualSessionOrder(projects, topic.WorkspaceRoot)) {
			node.SortOrder = -1
		}
		if node.Preview == "" && len(topic.Sessions) == 1 {
			node.Preview = base.Preview
		}
		nodes = append(nodes, node)
	}
	if len(nodes) == 0 {
		// Pinned and runtime-only topic shells remain reachable while their
		// catalog session is incomplete.
		return []ProjectNode{base}
	}
	manual := manualSessionOrderFor(topic.Scope, topic.WorkspaceRoot)
	sort.SliceStable(nodes, func(i, j int) bool {
		if manual && nodes[i].SortOrder != nodes[j].SortOrder {
			left, right := nodes[i].SortOrder, nodes[j].SortOrder
			if left < 0 {
				left = int(^uint(0) >> 1)
			}
			if right < 0 {
				right = int(^uint(0) >> 1)
			}
			return left < right
		}
		if nodes[i].LastActivityAt != nodes[j].LastActivityAt {
			return nodes[i].LastActivityAt > nodes[j].LastActivityAt
		}
		if nodes[i].CreatedAt != nodes[j].CreatedAt {
			return nodes[i].CreatedAt > nodes[j].CreatedAt
		}
		return nodes[i].Key < nodes[j].Key
	})
	return nodes
}

func projectManualSessionOrder(projects desktopProjectFile, workspaceRoot string) bool {
	index := projectIndexByRoot(projects.Projects, workspaceRoot)
	return index >= 0 && projects.Projects[index].ManualSessionOrder
}

func hiddenLegacyRecovery(record sessioncatalog.SessionRecord, localPreferred map[string]struct{}) bool {
	if record.RecoveryCopy || record.RecoveryRole == sessioncatalog.RecoveryRoleCoveredCopy {
		return true
	}
	if !record.Recovered || localPreferred == nil {
		return false
	}
	_, visible := localPreferred[strings.TrimSpace(record.Path)]
	return !visible
}

func coveredLegacyRuntime(records []sessioncatalog.SessionRecord, sessionOverlays map[string]catalogRuntimeOverlay, localPreferred map[string]struct{}) map[string]catalogRuntimeOverlay {
	coveredRuntime := map[string]catalogRuntimeOverlay{}
	for _, record := range records {
		if !hiddenLegacyRecovery(record, localPreferred) {
			continue
		}
		overlay := sessionOverlays[sessionRuntimeKey(record.Path)]
		if !overlay.open && !overlay.running {
			continue
		}
		parentID := strings.TrimSpace(record.ParentID)
		if parentID == "" {
			parentID = strings.TrimSpace(record.RecoveryGroupID)
		}
		if parentID == "" {
			continue
		}
		current := coveredRuntime[parentID]
		current.open = current.open || overlay.open
		current.running = current.running || overlay.running
		if current.status == "" {
			current.status = overlay.status
		}
		coveredRuntime[parentID] = current
	}
	return coveredRuntime
}
