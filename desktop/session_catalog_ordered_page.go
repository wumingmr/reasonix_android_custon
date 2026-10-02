package main

import (
	"fmt"
	"reasonix/internal/sessioncatalog"
	"sort"
	"strconv"
	"strings"
)

func (a *App) catalogSessionOrderedPage(catalog *sessioncatalog.Catalog, req ProjectTopicPageRequest) (ProjectTopicPage, error) {
	out := ProjectTopicPage{Items: []ProjectNode{}}
	topicOverlays, sessionOverlays := a.catalogRuntimeOverlays()
	ctx, cancel := a.catalogReadContext()
	defer cancel()
	if req.readContext != nil {
		ctx = req.readContext
	}
	preferred, _ := catalog.PreferredOrdinarySessionPaths(ctx, req.Scope, req.WorkspaceRoot)
	cursor := ""
	for {
		page, err := catalog.ListTopics(ctx, sessioncatalog.TopicPageRequest{
			Scope: req.Scope, WorkspaceRoot: req.WorkspaceRoot, Cursor: cursor,
			Limit: sessioncatalog.MaxLimit, Query: req.Query, TimeFilter: req.TimeFilter, SortMode: req.SortMode,
			ManualOrder: false, IncludeTopicIDsJSON: req.groupIncludeJSON,
			ExcludeTopicIDsJSON: req.groupExcludeJSON, ExcludePinned: req.ExcludePinned,
			CursorBinding: req.groupCursorBind,
		})
		if err != nil {
			return out, err
		}
		out.Revision = max(out.Revision, page.Revision)
		for _, topic := range page.Items {
			for _, node := range a.projectNodesFromCatalogTopic(topic, topicOverlays, sessionOverlays, preferred) {
				if projectNodeRequestAllows(req, node) {
					out.Items = append(out.Items, node)
				}
			}
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			return ProjectTopicPage{Items: []ProjectNode{}}, fmt.Errorf("catalog session-order cursor did not advance")
		}
		cursor = page.NextCursor
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		return projectTopicLess(out.Items[i], out.Items[j], req.SortMode, true)
	})
	offset := 0
	if strings.TrimSpace(req.Cursor) != "" {
		var err error
		if !strings.HasPrefix(req.Cursor, "session-order:") {
			return ProjectTopicPage{Items: []ProjectNode{}}, fmt.Errorf("session order cursor is stale")
		}
		offset, err = strconv.Atoi(strings.TrimPrefix(req.Cursor, "session-order:"))
		if err != nil || offset < 0 || offset > len(out.Items) {
			return ProjectTopicPage{Items: []ProjectNode{}}, fmt.Errorf("invalid session order cursor")
		}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = sessioncatalog.DefaultLimit
	}
	limit = min(limit, sessioncatalog.MaxLimit)
	end := min(offset+limit, len(out.Items))
	page := ProjectTopicPage{Items: append([]ProjectNode(nil), out.Items[offset:end]...), Revision: out.Revision}
	if end < len(out.Items) {
		page.NextCursor = "session-order:" + strconv.Itoa(end)
	}
	return page, nil
}
