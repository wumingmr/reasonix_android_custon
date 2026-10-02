package main

import (
	"fmt"
	"sort"
	"strings"

	"reasonix/internal/sessioncatalog"
)

func (a *App) metadataTopicPage(req ProjectTopicPageRequest) (ProjectTopicPage, error) {
	var items []ProjectNode
	if req.metadataSnapshot != nil {
		items = cloneTopicPage(*req.metadataSnapshot)
	} else {
		items = a.metadataProjectTopics(req.Scope, req.WorkspaceRoot)
	}
	filteredByGroup := items[:0]
	for _, item := range items {
		if projectTopicRequestAllows(req, item.TopicID, item.Pinned) && projectNodeRequestAllows(req, item) {
			filteredByGroup = append(filteredByGroup, item)
		}
	}
	items = filteredByGroup
	manualOrder := manualTopicOrderFor(req.Scope, req.WorkspaceRoot)
	if req.readAllSources {
		manualOrder = false
	}
	query := strings.ToLower(strings.TrimSpace(req.Query))
	if query != "" {
		filtered := items[:0]
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.Label), query) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	sort.SliceStable(items, func(i, j int) bool {
		return projectTopicLess(items[i], items[j], req.SortMode, manualOrder)
	})
	start := 0
	if lastID, ok := strings.CutPrefix(req.Cursor, "meta:"); ok {
		if req.groupCursorBind != "" {
			return ProjectTopicPage{Items: []ProjectNode{}}, fmt.Errorf("project topic cursor filter changed")
		}
		for index, item := range items {
			if item.TopicID == lastID {
				start = index + 1
				break
			}
		}
	} else if strings.TrimSpace(req.Cursor) != "" {
		start = len(items)
		for index, item := range items {
			var after bool
			var err error
			if manualOrder {
				after, err = sessioncatalog.TopicSortKeyAfterOrderedCursorBound(
					req.Cursor, req.groupCursorBind, item.Pinned, item.SortOrder,
					projectTopicSortValue(item.CreatedAt, item.LastActivityAt, req.SortMode), item.TopicID,
				)
			} else {
				after, err = sessioncatalog.TopicSortKeyAfterCursorBound(
					req.Cursor, req.groupCursorBind, item.Pinned,
					projectTopicSortValue(item.CreatedAt, item.LastActivityAt, req.SortMode), item.TopicID,
				)
			}
			if err != nil {
				return ProjectTopicPage{Items: []ProjectNode{}}, err
			}
			if after {
				start = index
				break
			}
		}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = sessioncatalog.DefaultLimit
	}
	if limit > sessioncatalog.MaxLimit {
		limit = sessioncatalog.MaxLimit
	}
	end := min(start+limit, len(items))
	page := ProjectTopicPage{Items: append([]ProjectNode(nil), items[start:end]...)}
	if end < len(items) && end > start {
		page.NextCursor = encodeProjectNodeCursor(items[end-1], req.SortMode, manualOrder, req.groupCursorBind)
	}
	return page, nil
}
