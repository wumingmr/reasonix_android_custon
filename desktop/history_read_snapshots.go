package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"reasonix/internal/sessioncatalog"
)

func snapshotReadError(err error) (*ReadError, bool) {
	if err == nil {
		return nil, false
	}
	var op *SessionOperationError
	if errors.As(err, &op) && op.Code == "stale_cursor" {
		return &ReadError{Code: "stale_cursor", Reason: op.ReadReason, Message: err.Error()}, true
	}
	return &ReadError{Code: "read_failed", Reason: "read_failed", Message: err.Error()}, false
}

func (a *App) ListHistorySessions(req HistorySessionPageRequest) HistorySessionPage {
	out := HistorySessionPage{Items: []SessionMeta{}}
	key := req
	key.Cursor, key.Limit = "", 0
	active := a.activeSessionPath(a.activeSessionDir())
	binding := snapshotBinding("history-sessions", []any{key, active})
	store := &a.desktopSessions.readSnapshots
	var first *readSnapshot
	var err error
	if req.Cursor == "" {
		first, err = store.build(a.bootContext(), binding, func(ctx context.Context, snap *readSnapshot) error {
			meta := HistorySessionPage{Partial: true}
			catalog := a.sessionCatalog.Load()
			if catalog == nil {
				snap.metadata, _ = json.Marshal(meta)
				return nil
			}
			_, overlays := a.catalogRuntimeOverlays()
			fence, err := a.newReadSourceFence(store, snap)
			if err != nil {
				return err
			}
			fence.metadataOnly = true
			err = catalog.WithReadView(ctx, func(view context.Context) error {
				cursor := ""
				for {
					page, err := catalog.ListSessions(view, sessioncatalog.SessionPageRequest{Scope: req.Scope, WorkspaceRoot: req.WorkspaceRoot, Query: req.Query, TimeFilter: req.TimeFilter, Cursor: cursor, Limit: 200})
					if err != nil {
						return err
					}
					if page.StaleCursor {
						return snapshotStale("lifecycle_changed")
					}
					meta.Revision = page.Revision
					for _, row := range page.Items {
						overlay := overlays[sessionRuntimeKey(row.Path)]
						if !historyStatusMatches(req.Status, overlay.open, row.Path == active) {
							continue
						}
						if err := fence.add(view, row.Path); err != nil {
							return err
						}
						if err := store.append(ctx, snap, sessionMetaFromCatalog(row, row.Path == active, overlay.open)); err != nil {
							return err
						}
					}
					if page.NextCursor == "" {
						return nil
					}
					if page.NextCursor == cursor {
						return errors.New("history catalog cursor did not advance")
					}
					cursor = page.NextCursor
				}
			})
			if err != nil {
				return err
			}
			status := catalog.Status()
			snap.validate = fence.freeze()
			meta.Partial = status.State != sessioncatalog.StateReady || status.Indexed < status.Total
			snap.metadata, err = json.Marshal(meta)
			return err
		})
	}
	if err == nil {
		var meta json.RawMessage
		out.NextCursor, out.SnapshotID, out.SnapshotExpiresAt, meta, err = store.page(a.bootContext(), binding, req.Cursor, first, req.Limit, func(b []byte) error {
			var row SessionMeta
			if err := json.Unmarshal(b, &row); err != nil {
				return err
			}
			out.Items = append(out.Items, row)
			return nil
		})
		if err == nil {
			var frozen HistorySessionPage
			err = json.Unmarshal(meta, &frozen)
			out.Revision, out.Partial = frozen.Revision, frozen.Partial
		}
	}
	if err != nil {
		out = HistorySessionPage{Items: []SessionMeta{}}
		out.ReadError, out.StaleCursor = snapshotReadError(err)
	}
	return out
}

func (a *App) SearchHistoryContent(req HistorySearchRequest) HistorySearchPage {
	return a.searchHistorySnapshot(req, "")
}

func (a *App) searchHistorySnapshot(req HistorySearchRequest, targetPath string) HistorySearchPage {
	out := HistorySearchPage{Items: []HistorySearchHit{}}
	req.Query = strings.TrimSpace(req.Query)
	req.Kinds = append([]string(nil), req.Kinds...)
	if len(req.Kinds) == 0 {
		// The panel searches what the user saw. Every tool result is indexed as
		// tool_output, errors included, so adding tool_error would list them twice.
		req.Kinds = []string{"user_text", "assistant_text", "tool_input", "tool_output"}
	}
	sort.Strings(req.Kinds)
	key := req
	key.Cursor, key.Limit = "", 0
	active := a.activeSessionPath(a.activeSessionDir())
	activeBinding := active
	if targetPath != "" {
		// Exact-target reads belong to the selected source, not foreground focus.
		activeBinding = ""
	}
	binding := snapshotBinding("history-search", []any{key, targetPath, activeBinding})
	store := &a.desktopSessions.readSnapshots
	var first *readSnapshot
	var err error
	if req.Cursor == "" {
		first, err = store.build(a.bootContext(), binding, func(ctx context.Context, snap *readSnapshot) error {
			return a.buildHistorySearchSnapshot(ctx, store, snap, req, targetPath, active)
		})
	}
	if err == nil {
		var meta json.RawMessage
		out.NextCursor, out.SnapshotID, out.SnapshotExpiresAt, meta, err = store.page(a.bootContext(), binding, req.Cursor, first, req.Limit, func(b []byte) error {
			var row HistorySearchHit
			if err := json.Unmarshal(b, &row); err != nil {
				return err
			}
			out.Items = append(out.Items, row)
			return nil
		})
		if err == nil {
			var frozen HistorySearchPage
			err = json.Unmarshal(meta, &frozen)
			out.Revision, out.Partial, out.Status = frozen.Revision, frozen.Partial, frozen.Status
		}
	}
	if err != nil {
		out = HistorySearchPage{Items: []HistorySearchHit{}}
		out.ReadError, out.StaleCursor = snapshotReadError(err)
		out.Status.LastError = err.Error()
	}
	return out
}
