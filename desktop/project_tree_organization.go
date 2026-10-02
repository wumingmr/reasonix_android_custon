package main

import (
	"fmt"
	"reasonix/desktop/internal/workspacestate"
	"strings"
	"unicode/utf8"
)

const (
	maxSessionGroups       = 100
	maxSessionGroupIDRunes = 160
	maxSessionGroupRunes   = 120
	maxSessionGroupTopics  = 10_000
)

type desktopProject struct {
	Root               string         `json:"root"`
	Title              string         `json:"title,omitempty"`
	Color              string         `json:"color,omitempty"`
	Topics             []string       `json:"topics"`
	PinnedTopics       []string       `json:"pinnedTopics,omitempty"`
	ManualTopicOrder   bool           `json:"manualTopicOrder,omitempty"`
	SessionOrder       []string       `json:"sessionOrder,omitempty"`
	ManualSessionOrder bool           `json:"manualSessionOrder,omitempty"`
	Groups             []desktopGroup `json:"groups,omitempty"`
	GroupsRevision     uint64         `json:"-"`
}

type desktopGroup struct {
	ID                  string   `json:"id"`
	Title               string   `json:"title"`
	TopicIDs            []string `json:"topicIds,omitempty"`
	SessionKeys         []string `json:"sessionKeys,omitempty"`
	ExcludedSessionKeys []string `json:"excludedSessionKeys,omitempty"`
}

type ProjectGroupsSnapshot struct {
	Groups   []desktopGroup `json:"groups"`
	Revision uint64         `json:"revision"`
	Applied  bool           `json:"applied"`
}

type desktopProjectFile struct {
	GlobalTitle              string           `json:"globalTitle,omitempty"`
	GlobalColor              string           `json:"globalColor,omitempty"`
	GlobalTopics             []string         `json:"globalTopics,omitempty"`
	GlobalPinnedTopics       []string         `json:"globalPinnedTopics,omitempty"`
	GlobalManualTopicOrder   bool             `json:"globalManualTopicOrder,omitempty"`
	GlobalSessionOrder       []string         `json:"globalSessionOrder,omitempty"`
	GlobalManualSessionOrder bool             `json:"globalManualSessionOrder,omitempty"`
	GlobalGroups             []desktopGroup   `json:"globalGroups,omitempty"`
	GlobalGroupsRevision     uint64           `json:"-"`
	DeletedTopics            []string         `json:"deletedTopics,omitempty"`
	PinnedProjects           []string         `json:"pinnedProjects,omitempty"`
	SidebarOrder             []string         `json:"sidebarOrder,omitempty"`
	Projects                 []desktopProject `json:"projects"`
}

// ReorderSessions enables stable per-session manual ordering. Older topic
// order fields remain available for downgrade compatibility and for rows that
// have not yet acquired an explicit session identity.
func (a *App) ReorderSessions(scope, workspaceRoot string, orderedSessionKeys []string) error {
	id, _, err := a.ensureSessionOrganizationSources(scope, workspaceRoot, true)
	if err != nil {
		return err
	}
	_, _, err = a.workspaceRegistry().UpdateOrganization(a.bootContext(), id, nil, func(o *workspacestate.Organization) error {
		if len(orderedSessionKeys) == 0 {
			return fmt.Errorf("orderedSessionKeys is required")
		}
		seen := map[string]bool{}
		for _, key := range orderedSessionKeys {
			if key == "" || seen[key] || !o.Imported[key] {
				return fmt.Errorf("unknown or duplicate session order key")
			}
			seen[key] = true
		}
		i := 0
		for j, key := range o.Order {
			if seen[key] {
				o.Order[j] = orderedSessionKeys[i]
				i++
			}
		}
		o.ManualOrderEnabled = true
		return nil
	})
	if err == nil {
		a.emitProjectTreeMetadataChanged()
	}
	return err
}

func normalizeGroups(groups []desktopGroup) []desktopGroup {
	out := make([]desktopGroup, 0, len(groups))
	seenGroups := make(map[string]bool, len(groups))
	seenTopics := make(map[string]bool)
	seenSessions := make(map[string]bool)
	for _, group := range groups {
		group.ID = strings.TrimSpace(group.ID)
		group.Title = strings.TrimSpace(group.Title)
		if group.ID == "" || seenGroups[group.ID] {
			continue
		}
		seenGroups[group.ID] = true
		topics := make([]string, 0, len(group.TopicIDs))
		for _, topicID := range group.TopicIDs {
			topicID = strings.TrimSpace(topicID)
			if topicID == "" || seenTopics[topicID] {
				continue
			}
			seenTopics[topicID] = true
			topics = append(topics, topicID)
		}
		group.TopicIDs = topics
		var sessions []string
		if len(group.SessionKeys) > 0 {
			sessions = make([]string, 0, len(group.SessionKeys))
		}
		for _, key := range group.SessionKeys {
			key = strings.TrimSpace(key)
			if key == "" || seenSessions[key] {
				continue
			}
			seenSessions[key] = true
			sessions = append(sessions, key)
		}
		group.SessionKeys = sessions
		var excluded []string
		if len(group.ExcludedSessionKeys) > 0 {
			excluded = make([]string, 0, len(group.ExcludedSessionKeys))
		}
		seenExcluded := map[string]bool{}
		for _, key := range group.ExcludedSessionKeys {
			key = strings.TrimSpace(key)
			if key == "" || seenExcluded[key] {
				continue
			}
			seenExcluded[key] = true
			excluded = append(excluded, key)
		}
		group.ExcludedSessionKeys = excluded
		out = append(out, group)
	}
	return out
}

func mergeDesktopGroups(left, right []desktopGroup) []desktopGroup {
	merged := append(append([]desktopGroup(nil), left...), right...)
	byID := make(map[string]int, len(merged))
	out := make([]desktopGroup, 0, len(merged))
	for _, group := range merged {
		group.ID = strings.TrimSpace(group.ID)
		if group.ID == "" {
			continue
		}
		if index, ok := byID[group.ID]; ok {
			if out[index].Title == "" {
				out[index].Title = strings.TrimSpace(group.Title)
			}
			out[index].TopicIDs = append(out[index].TopicIDs, group.TopicIDs...)
			out[index].SessionKeys = append(out[index].SessionKeys, group.SessionKeys...)
			out[index].ExcludedSessionKeys = append(out[index].ExcludedSessionKeys, group.ExcludedSessionKeys...)
			continue
		}
		byID[group.ID] = len(out)
		out = append(out, group)
	}
	return normalizeGroups(out)
}

func validateSessionGroups(groups []desktopGroup) error {
	if len(groups) > maxSessionGroups {
		return fmt.Errorf("too many session groups: %d", len(groups))
	}
	seenGroups := make(map[string]bool, len(groups))
	seenTopics := make(map[string]bool)
	seenSessions := make(map[string]bool)
	for _, group := range groups {
		id, title := strings.TrimSpace(group.ID), strings.TrimSpace(group.Title)
		if id == "" || title == "" {
			return fmt.Errorf("session group id and title are required")
		}
		if utf8.RuneCountInString(id) > maxSessionGroupIDRunes || utf8.RuneCountInString(title) > maxSessionGroupRunes {
			return fmt.Errorf("session group id or title is too long")
		}
		if seenGroups[id] {
			return fmt.Errorf("duplicate session group %q", id)
		}
		seenGroups[id] = true
		if len(group.TopicIDs)+len(group.SessionKeys)+len(group.ExcludedSessionKeys) > maxSessionGroupTopics {
			return fmt.Errorf("session group %q has too many topics", id)
		}
		for _, topicID := range group.TopicIDs {
			topicID = strings.TrimSpace(topicID)
			if topicID == "" || seenTopics[topicID] {
				return fmt.Errorf("invalid or duplicate grouped topic %q", topicID)
			}
			seenTopics[topicID] = true
		}
		for _, key := range group.SessionKeys {
			key = strings.TrimSpace(key)
			if key == "" || seenSessions[key] {
				return fmt.Errorf("invalid or duplicate grouped session %q", key)
			}
			seenSessions[key] = true
		}
		seenExcluded := map[string]bool{}
		for _, key := range group.ExcludedSessionKeys {
			key = strings.TrimSpace(key)
			if key == "" || seenExcluded[key] {
				return fmt.Errorf("invalid or duplicate excluded session %q", key)
			}
			seenExcluded[key] = true
		}
	}
	return nil
}

func completeTopicOrder(orderedTopicIDs, previous []string) ([]string, error) {
	available := make(map[string]bool, len(previous))
	for _, id := range previous {
		available[id] = true
	}
	seen := make(map[string]bool, len(orderedTopicIDs))
	ordered := make([]string, 0, len(orderedTopicIDs))
	for _, id := range orderedTopicIDs {
		id = strings.TrimSpace(id)
		if id == "" || !available[id] {
			return nil, fmt.Errorf("unknown topic %q", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate topic %q", id)
		}
		seen[id] = true
		ordered = append(ordered, id)
	}
	// A paged sidebar only sends the rows it has loaded. Replace those rows in
	// their existing slots so omitted topics keep both their relative order and
	// their position among the visible subset.
	next := append([]string(nil), previous...)
	orderedIndex := 0
	for index, id := range previous {
		if seen[id] {
			next[index] = ordered[orderedIndex]
			orderedIndex++
		}
	}
	return next, nil
}

func normalizeOrganizationTarget(scope, workspaceRoot string) (string, string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "global" {
		return scope, "", nil
	}
	if scope != "project" {
		return "", "", fmt.Errorf("unsupported scope %q", scope)
	}
	workspaceRoot = normalizeProjectRoot(workspaceRoot)
	if workspaceRoot == "" {
		return "", "", fmt.Errorf("workspaceRoot is required for project scope")
	}
	return scope, workspaceRoot, nil
}

// ReorderTopics persists a manual order without dropping topics omitted by a
// partially loaded client. The manual flag preserves activity sorting for
// existing users until they explicitly drag a topic.
func (a *App) ReorderTopics(scope, workspaceRoot string, orderedTopicIDs []string) error {
	scope, workspaceRoot, err := normalizeOrganizationTarget(scope, workspaceRoot)
	if err != nil {
		return err
	}
	if len(orderedTopicIDs) == 0 {
		return fmt.Errorf("orderedTopicIDs is required")
	}
	if err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		if scope == "global" {
			next, orderErr := completeTopicOrder(orderedTopicIDs, f.GlobalTopics)
			if orderErr != nil {
				return false, orderErr
			}
			changed := !sameStringList(next, f.GlobalTopics) || !f.GlobalManualTopicOrder
			f.GlobalTopics, f.GlobalManualTopicOrder = next, true
			return changed, nil
		}
		i := projectIndexByRoot(f.Projects, workspaceRoot)
		if i < 0 {
			return false, fmt.Errorf("project %q not found", workspaceRoot)
		}
		next, orderErr := completeTopicOrder(orderedTopicIDs, f.Projects[i].Topics)
		if orderErr != nil {
			return false, orderErr
		}
		changed := !sameStringList(next, f.Projects[i].Topics) || !f.Projects[i].ManualTopicOrder
		f.Projects[i].Topics, f.Projects[i].ManualTopicOrder = next, true
		return changed, nil
	}); err != nil {
		return err
	}
	a.emitProjectTreeMetadataChanged()
	return nil
}

func (a *App) ListProjectGroups(scope, workspaceRoot string) ([]desktopGroup, error) {
	snapshot, err := a.GetSessionOrganization(SessionOrganizationWorkspace{Scope: scope, WorkspaceRoot: workspaceRoot})
	return snapshot.Groups, err
}

func nonNilGroups(groups []desktopGroup) []desktopGroup {
	groups = normalizeGroups(groups)
	if groups == nil {
		return []desktopGroup{}
	}
	return groups
}

// GetProjectGroups is the versioned organization read used by current
// frontends. ListProjectGroups remains for old binaries during the transition.
func (a *App) GetProjectGroups(scope, workspaceRoot string) (ProjectGroupsSnapshot, error) {
	snapshot, err := a.GetSessionOrganization(SessionOrganizationWorkspace{Scope: scope, WorkspaceRoot: workspaceRoot})
	return ProjectGroupsSnapshot{Groups: snapshot.Groups, Revision: snapshot.Revision, Applied: snapshot.Applied}, err
}

func (a *App) SaveSessionGroups(scope, workspaceRoot string, groups []desktopGroup) error {
	_, err := a.replaceSessionOrganizationGroups(a.bootContext(), scope, workspaceRoot, nil, groups)
	return err
}

// SaveSessionGroupsVersioned is a compare-and-swap over one workspace. It
// prevents two windows (and archive cleanup) from overwriting each other's
// full group snapshots. On conflict the current state is returned so the
// frontend can reapply its semantic mutation and retry.
func (a *App) SaveSessionGroupsVersioned(scope, workspaceRoot string, expectedRevision uint64, groups []desktopGroup) (ProjectGroupsSnapshot, error) {
	return a.replaceSessionOrganizationGroups(a.bootContext(), scope, workspaceRoot, &expectedRevision, groups)
}

func groupsWithoutTopic(groups []desktopGroup, topicID string) ([]desktopGroup, bool) {
	next := make([]desktopGroup, len(groups))
	changed := false
	for i, group := range groups {
		next[i] = group
		next[i].TopicIDs = removeString(group.TopicIDs, topicID)
		changed = changed || !sameStringList(next[i].TopicIDs, group.TopicIDs)
	}
	return next, changed
}
