package main

import (
	"context"
	"encoding/json"
	"reasonix/internal/sessioncatalog"
)

// The snapshot lease guards both fixed query inputs and cursor checkpoints.
type lazyTopicPageReader struct {
	app         *App
	catalog     *sessioncatalog.Catalog
	lease       *sessioncatalog.ReadLease
	req         ProjectTopicPageRequest
	query       sessioncatalog.OrdinaryPageRequest
	extras      []ProjectNode
	positions   map[int]topicPagePosition
	match       func(sessioncatalog.OrdinaryRecord) bool
	recordTitle func(sessioncatalog.OrdinaryRecord) string
	less        func(ProjectNode, ProjectNode) bool
	fence       *readSourceFence
}

func (r *lazyTopicPageReader) page(readCtx context.Context, offset, limit int) (result [][]byte, hasMore bool, resultErr error) {
	select {
	case <-r.catalog.Invalidated():
		return nil, false, snapshotStale("catalog_replaced")
	default:
	}
	defer func() {
		select {
		case <-r.catalog.Invalidated():
			result, hasMore, resultErr = nil, false, snapshotStale("catalog_replaced")
		default:
		}
	}()
	a, catalog, lease, req := r.app, r.catalog, r.lease, r.req
	query, extras, positions := r.query, r.extras, r.positions
	match, recordTitle, less := r.match, r.recordTitle, r.less
	fence, store, snap := r.fence, r.fence.store, r.fence.snapshot
	position, ok := positions[offset]
	if !ok {
		return nil, false, snapshotStale("invalid_cursor")
	}
	request := query
	request.Cursor, request.Limit = position.cursor, min(limit+1, sessioncatalog.MaxLimit)
	records, err := catalog.ListMatchingOrdinarySessions(lease.Context(readCtx), request, match)
	if err != nil {
		return nil, false, err
	}
	_, runtime := a.catalogRuntimeOverlays()
	legacy := make([]ProjectNode, 0, len(records))
	for _, record := range records {
		overlay := runtime[sessionRuntimeKey(record.Path)]
		kind := "topic"
		if req.Scope == "global" {
			kind = "global_topic"
		}
		title := recordTitle(record)
		legacy = append(legacy, ProjectNode{Key: projectSessionNodeKey(req.Scope, record.Path), Kind: kind, Label: title, Root: req.WorkspaceRoot, TopicID: record.TopicID, SessionPath: record.Path,
			Historical: true, Source: &SessionSourceRef{HostID: localDesktopHostID, Path: record.Path, SourceKey: record.SourceKey()},
			Preview: record.Preview, Turns: record.Turns, TurnsState: string(record.TurnsState), Health: string(record.Health), CreatedAt: record.CreatedAt, LastActivityAt: record.LastActivityAt, Pinned: record.Pinned, SortOrder: -1,
			Recovered: record.Recovered, RecoveryReason: record.RecoveryReason, RecoveryDigest: record.RecoveryDigest, RecoveryParentID: record.ParentID, Open: overlay.open, Running: overlay.running, Status: overlay.status, Children: []ProjectNode{}})
	}
	rows := [][]byte{}
	index := 0
	for len(rows) < limit && (index < len(legacy) || position.extra < len(extras)) {
		var node ProjectNode
		if index < len(legacy) && (position.extra >= len(extras) || less(legacy[index], extras[position.extra])) {
			node = legacy[index]
			position.cursor = records[index].Cursor
			index++
		} else {
			node = extras[position.extra]
			position.extra++
		}
		b, err := json.Marshal(node)
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, b)
		if node.Session == nil && node.SessionPath != "" {
			if err := fence.add(lease.Context(readCtx), node.SessionPath); err != nil {
				return nil, false, err
			}
		}
	}
	more := index < len(legacy) || position.extra < len(extras) || len(records) == request.Limit
	if more {
		if _, exists := positions[offset+len(rows)]; !exists {
			if err := store.reserve(snap, int64(len(position.cursor)+64)); err != nil {
				return nil, false, err
			}
			positions[offset+len(rows)] = position
		}
	}
	return rows, more, nil
}
