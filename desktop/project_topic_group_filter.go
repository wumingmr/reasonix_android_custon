package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

func resolveProjectTopicGroupFilter(req *ProjectTopicPageRequest) error {
	requestedFilter := strings.TrimSpace(strings.ToLower(req.GroupFilter))
	filter := requestedFilter
	if filter == "" {
		filter = "all"
	}
	if filter != "all" && filter != "ungrouped" && filter != "group" {
		return fmt.Errorf("invalid project topic group filter %q", req.GroupFilter)
	}
	if filter == "all" {
		req.GroupFilter = "all"
		// An omitted filter keeps the legacy unbound cursor contract. Every
		// caller that opts into the new sidebar fields receives a cursor bound
		// to the complete list identity, including project, query and sort.
		if requestedFilter != "" || req.ExcludePinned {
			req.groupCursorBind = projectTopicCursorBinding(*req, filter, "", 0)
		}
		return nil
	}

	f := loadProjectsFile()
	groups := f.GlobalGroups
	revision := f.GlobalGroupsRevision
	if strings.TrimSpace(req.Scope) == "project" {
		index := projectIndexByRoot(f.Projects, req.WorkspaceRoot)
		if index >= 0 {
			groups = f.Projects[index].Groups
			revision = f.Projects[index].GroupsRevision
		} else {
			groups = nil
		}
	}
	groups = normalizeGroups(groups)
	req.GroupFilter = filter
	req.groupAll = append([]desktopGroup(nil), groups...)

	if filter == "group" {
		groupID := strings.TrimSpace(req.GroupID)
		if groupID == "" {
			return fmt.Errorf("project topic group id is required")
		}
		for _, group := range groups {
			if group.ID != groupID {
				continue
			}
			selected := group
			req.groupSelected = &selected
			if !groupHasSessionRules(group) {
				req.groupIncludeJSON, req.groupInclude = topicIDFilter(group.TopicIDs)
			}
			req.groupCursorBind = projectTopicCursorBinding(*req, filter, groupID, revision)
			return nil
		}
		return fmt.Errorf("project topic group %q no longer exists", groupID)
	}

	allGrouped := make([]string, 0)
	for _, group := range groups {
		allGrouped = append(allGrouped, group.TopicIDs...)
	}
	if !groupsHaveSessionRules(groups) {
		req.groupExcludeJSON, req.groupExclude = topicIDFilter(allGrouped)
	}
	req.groupCursorBind = projectTopicCursorBinding(*req, filter, "", revision)
	return nil
}

func projectTopicCursorBinding(req ProjectTopicPageRequest, filter, groupID string, membershipRevision uint64) string {
	payload, _ := json.Marshal([]any{
		"project-topics-v1",
		strings.TrimSpace(strings.ToLower(req.Scope)),
		strings.TrimSpace(req.WorkspaceRoot),
		strings.TrimSpace(strings.ToLower(req.Query)),
		strings.TrimSpace(strings.ToLower(req.TimeFilter)),
		strings.TrimSpace(strings.ToLower(req.SortMode)),
		filter,
		strings.TrimSpace(groupID),
		membershipRevision,
		req.ExcludePinned,
		req.pinnedOnly,
	})
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("%x", digest[:])
}

func topicIDFilter(ids []string) (string, map[string]struct{}) {
	set := make(map[string]struct{}, len(ids))
	ordered := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := set[id]; exists {
			continue
		}
		set[id] = struct{}{}
		ordered = append(ordered, id)
	}
	encoded, _ := json.Marshal(ordered)
	return string(encoded), set
}

func projectTopicRequestAllows(req ProjectTopicPageRequest, topicID string, pinned bool) bool {
	if req.pinnedOnly && !pinned {
		return false
	}
	if req.ExcludePinned && pinned {
		return false
	}
	if req.groupInclude != nil {
		_, ok := req.groupInclude[topicID]
		return ok
	}
	if req.groupExclude != nil {
		_, excluded := req.groupExclude[topicID]
		return !excluded
	}
	return true
}

func groupHasSessionRules(group desktopGroup) bool {
	return len(group.SessionKeys) > 0 || len(group.ExcludedSessionKeys) > 0
}

func groupsHaveSessionRules(groups []desktopGroup) bool {
	return slices.ContainsFunc(groups, groupHasSessionRules)
}

func projectNodeSessionKey(node ProjectNode) string {
	if node.Session != nil && strings.TrimSpace(node.Session.SessionID) != "" {
		hostID := strings.TrimSpace(node.Session.HostID)
		if hostID == "" {
			hostID = localDesktopHostID
		}
		return "ref\x00" + hostID + "\x00" + strings.TrimSpace(node.Session.SessionID)
	}
	if node.Source != nil && node.Source.SourceKey != "" {
		host := node.Source.HostID
		if host == "" {
			host = localDesktopHostID
		}
		return "source\x00" + host + "\x00" + node.Source.SourceKey
	}
	if path := strings.TrimSpace(node.SessionPath); path != "" {
		return "path\x00" + path
	}
	return "topic\x00" + firstNonEmpty(strings.TrimSpace(node.TopicID), strings.TrimSpace(node.Key))
}

func desktopGroupContainsNode(group desktopGroup, node ProjectNode) bool {
	key := projectNodeSessionKey(node)
	for _, excluded := range group.ExcludedSessionKeys {
		if strings.TrimSpace(excluded) == key {
			return false
		}
	}
	for _, explicit := range group.SessionKeys {
		if strings.TrimSpace(explicit) == key {
			return true
		}
	}
	for _, topicID := range group.TopicIDs {
		if strings.TrimSpace(topicID) != "" && strings.TrimSpace(topicID) == strings.TrimSpace(node.TopicID) {
			return true
		}
	}
	return false
}

func projectNodeRequestAllows(req ProjectTopicPageRequest, node ProjectNode) bool {
	if req.pinnedOnly && !node.Pinned {
		return false
	}
	if req.ExcludePinned && node.Pinned {
		return false
	}
	switch req.GroupFilter {
	case "group":
		return req.groupSelected != nil && desktopGroupContainsNode(*req.groupSelected, node)
	case "ungrouped":
		for _, group := range req.groupAll {
			if desktopGroupContainsNode(group, node) {
				return false
			}
		}
		return true
	default:
		return true
	}
}
