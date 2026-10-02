package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/sessioncatalog"
)

type topicPagePosition struct {
	cursor string
	extra  int
}

// Ordinary legacy history is read directly from the existing catalog WAL
// snapshot. Only requested pages are decoded; no all-pages loop or temporary
// copy of the whole result is needed to return the first fifty rows.
func (a *App) lazyProjectTopicSnapshot(req ProjectTopicPageRequest, reader workspaceSessionInfoReader, snap *readSnapshot, state workspacestate.State, workspaceID string, org workspacestate.Organization, versions *workspacestate.ReadVersions) (ProjectTopicPage, func() error, bool, error) {
	catalog := a.sessionCatalog.Load()
	if catalog == nil || !catalog.MetadataOnly() || org.ManualOrderEnabled {
		return ProjectTopicPage{}, nil, false, nil
	}
	if err := applyOrganizationGroupFilter(&req, org); err != nil {
		return ProjectTopicPage{}, nil, true, err
	}
	// Freeze relative filters once so canonical extras and every catalog page
	// use the same boundary even if the cursor is resumed much later.
	req.timeCutoff = desktopSessionTimeCutoff(req.TimeFilter)
	lease, err := catalog.OpenReadLease(a.desktopSessions.readSnapshots.ctx)
	if errors.Is(err, sessioncatalog.ErrReadLeaseUnavailable) {
		return ProjectTopicPage{}, nil, false, nil
	}
	if err != nil {
		return ProjectTopicPage{}, nil, true, err
	}
	snap.closeRead = lease.Close
	ctx := lease.Context(a.bootContext())
	// Decide which adapter can represent this exact WAL snapshot. A head
	// discovered after this transaction starts belongs to the next refresh.
	if multiple, err := catalog.HasMultipleHeads(ctx, req.Scope, req.WorkspaceRoot); err != nil || multiple {
		lease.Close()
		snap.closeRead = nil
		return ProjectTopicPage{}, nil, err != nil, err
	}
	excluded, flat := lazyTopicSourceExclusions(state, workspaceID)
	if !flat {
		lease.Close()
		snap.closeRead = nil
		return ProjectTopicPage{}, nil, false, nil
	}
	workspace := state.Workspaces[workspaceID]
	workspace.SessionIDs = admittedWorkspaceTopicMembers(req, state, workspace)
	infos, _ := listWorkspaceSessionInfo(a.bootContext(), reader, workspace.SessionIDs)
	sources := a.historicalCanonicalTopicsFromProjection(req.Scope, req.WorkspaceRoot, state, workspacestate.NewWorkspaceIndex(state))
	if saved, err := readHistoricalSidecar(); err == nil {
		applyHistoricalPresentations(sources, saved)
	}
	adoptedTopics := state.AdoptedTopicIDs(workspaceID)
	// Unbacked user-created topics remain visible without inspecting a body.
	for _, node := range a.withRemovablePlaceholderTopics(req, state, nil, adoptedTopics) {
		found, err := catalog.HasTopicSessions(ctx, req.Scope, req.WorkspaceRoot, node.TopicID)
		if err != nil {
			return ProjectTopicPage{}, nil, true, err
		}
		if !found {
			sources = append(sources, node)
		}
	}
	extras := a.indexedWorkspaceTopics(req, state, workspace, org, infos, sources)
	// This comparator also covers ties between flat catalog rows. Physical
	// paths are stable identities; a background activity update cannot reorder
	// a view whose WAL transaction has already been captured.
	less := func(left, right ProjectNode) bool {
		if left.Pinned == right.Pinned && projectTopicSortValue(left.CreatedAt, left.LastActivityAt, req.SortMode) == projectTopicSortValue(right.CreatedAt, right.LastActivityAt, req.SortMode) && left.TopicID == right.TopicID {
			return topicPageIdentity(left) < topicPageIdentity(right)
		}
		return projectTopicLess(left, right, req.SortMode, false)
	}
	sort.SliceStable(extras, func(i, j int) bool { return less(extras[i], extras[j]) })
	excludedJSON, _ := json.Marshal(excluded)
	var deletedTopicsJSON []byte
	if deleted := loadProjectsFile().DeletedTopics; len(deleted) > 0 {
		deletedTopicsJSON, _ = json.Marshal(deleted)
	}
	query := sessioncatalog.OrdinaryPageRequest{Scope: req.Scope, WorkspaceRoot: req.WorkspaceRoot, SortMode: req.SortMode, PinnedOnly: req.pinnedOnly, ExcludePinned: req.ExcludePinned, ExcludedPathsJSON: string(excludedJSON), MinActivity: req.timeCutoff}
	query.ExcludedTopicIDsJSON = string(deletedTopicsJSON)
	groupJSON := ordinaryGroupSourceKeys(req)
	// The retained page closure only needs the encoded membership predicate.
	// Do not keep another copy of every organization's member slice alive.
	req.groupAll, req.groupSelected = nil, nil
	switch req.GroupFilter {
	case "group":
		query.IncludeSourceKeysJSON = groupJSON
	case "ungrouped":
		query.ExcludeSourceKeysJSON = groupJSON
	}
	query.ExcludeSourceKeysJSON = a.excludeUnavailableHistoricalSources(query.ExcludeSourceKeysJSON)
	// Freeze the localized default along with the search predicate. SQLite's
	// lower() does not implement the existing Go Unicode matching semantics.
	recordTitle := ordinaryRecordTitle(a.localizedDefaultTopicTitle())
	var match func(sessioncatalog.OrdinaryRecord) bool
	if text := strings.ToLower(strings.TrimSpace(req.Query)); text != "" {
		match = func(record sessioncatalog.OrdinaryRecord) bool {
			key := "source\x00" + localDesktopHostID + "\x00" + record.SourceKey()
			return strings.Contains(strings.ToLower(recordTitle(record)+"\n"+record.Preview+"\n"+key), text)
		}
	}
	// Snapshot memory accounts for retained metadata and cursor checkpoints,
	// including roots retained while a caller has not yet requested page two.
	store := &a.desktopSessions.readSnapshots
	fence := &readSourceFence{app: a, files: map[string]os.FileInfo{}, bindings: map[string]string{}, versions: versions, store: store, snapshot: snap, metadataOnly: true}
	encoded, err := json.Marshal(extras)
	if err != nil {
		return ProjectTopicPage{}, nil, true, err
	}
	if err := store.reserve(snap, int64(len(encoded)+len(excludedJSON)+len(deletedTopicsJSON)+len(groupJSON)+2*len(req.Query)+1024)); err != nil {
		return ProjectTopicPage{}, nil, true, err
	}
	snap.readPage = (&lazyTopicPageReader{app: a, catalog: catalog, lease: lease, req: req,
		query: query, extras: extras, positions: map[int]topicPagePosition{0: {}},
		match: match, recordTitle: recordTitle, less: less, fence: fence}).page
	availability := a.catalogWorkspaceAvailability(catalog, req.Scope, req.WorkspaceRoot, ctx)
	page := availability.decorate(ProjectTopicPage{Items: []ProjectNode{}}, catalog.Status().Revision+state.Generation)
	validateWorkspace := a.workspaceReadFence(versions, workspace, extras)
	return page, func() error {
		current, err := a.workspaceRegistry().VerifySnapshot(a.bootContext())
		if err != nil {
			return err
		}
		if err := validateWorkspace(current); err != nil {
			return err
		}
		return fence.validateWithCurrent(current)
	}, true, nil
}

func lazyTopicSourceExclusions(state workspacestate.State, workspaceID string) ([]string, bool) {
	excluded := []string{}
	for _, mapping := range state.SourceMappings {
		// Canonical directories have one source identity even if an older
		// migration receipt retained their originating head ID.
		if mapping.Format != "canonical" && !sourceMappingHasPathAlias(mapping) {
			if mapping.WorkspaceID != workspaceID {
				continue
			}
			// The catalog may not yet know about a new sibling, or the head
			// index may be unavailable. Only the head-aware adapter can filter
			// adopted identities without inventing a new path-only source.
			return nil, false
		}
		// A sidecar can list a file outside the workspace that adopted it.
		excluded = append(excluded, mapping.Path)
	}
	return excluded, true
}

// Group membership comes from the same immutable organization as the canonical
// rows. A group contains explicit session keys after preference import, so
// sessions sharing a topic must not inherit each other's membership.
func applyOrganizationGroupFilter(req *ProjectTopicPageRequest, org workspacestate.Organization) error {
	req.groupInclude, req.groupExclude = nil, nil
	req.groupIncludeJSON, req.groupExcludeJSON = "", ""
	req.groupAll = organizationSnapshot(org, true).Groups
	req.groupSelected = nil
	if req.GroupFilter == "group" {
		for i := range req.groupAll {
			if req.groupAll[i].ID == req.GroupID {
				req.groupSelected = &req.groupAll[i]
				return nil
			}
		}
		return fmt.Errorf("session group no longer exists")
	}
	return nil
}

func ordinaryGroupSourceKeys(req ProjectTopicPageRequest) string {
	if req.GroupFilter != "group" && req.GroupFilter != "ungrouped" {
		return ""
	}
	groups := req.groupAll
	if req.GroupFilter == "group" {
		groups = []desktopGroup{*req.groupSelected}
	}
	keys, seen := []string{}, map[string]bool{}
	const prefix = "source\x00" + localDesktopHostID + "\x00"
	for _, group := range groups {
		for _, member := range group.SessionKeys {
			if key, ok := strings.CutPrefix(strings.TrimSpace(member), prefix); ok && !seen[key] {
				keys, seen[key] = append(keys, key), true
			}
		}
	}
	encoded, _ := json.Marshal(keys)
	return string(encoded)
}

func topicPageIdentity(node ProjectNode) string {
	if node.Session != nil {
		return "ref\x00" + node.Session.HostID + "\x00" + node.Session.SessionID
	}
	if node.Source != nil {
		return "source\x00" + node.Source.Path + "\x00" + node.Source.HeadID
	}
	return "node\x00" + node.Key
}

func ordinaryRecordTitle(defaultTitle string) func(sessioncatalog.OrdinaryRecord) string {
	return func(record sessioncatalog.OrdinaryRecord) string {
		if title := strings.TrimSpace(record.CustomTitle); title != "" {
			return title
		}
		if strings.TrimSpace(record.TitleSource) == topicTitleSourceAuto && isDefaultTopicTitle(record.Title) {
			return defaultTitle
		}
		return record.Title
	}
}
