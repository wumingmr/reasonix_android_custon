package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type targetHistorySliceCursor struct {
	V      int    `json:"v"`
	Target string `json:"target"`
	Cursor string `json:"cursor"`
}

func decodeTargetHistorySliceCursor(raw, target string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", newSessionOperationError("stale_cursor", "The session content changed. Reload it and try again.")
	}
	var cursor targetHistorySliceCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.V != 1 || cursor.Target != target {
		return "", newSessionOperationError("stale_cursor", "The session content changed. Reload it and try again.")
	}
	return cursor.Cursor, nil
}

func encodeTargetHistorySliceCursor(target, cursor string) string {
	if strings.TrimSpace(cursor) == "" {
		return ""
	}
	data, err := json.Marshal(targetHistorySliceCursor{V: 1, Target: target, Cursor: cursor})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// HistorySliceForTarget reads a bounded history page for either a canonical or
// legacy local session without selecting it or creating a controller.
func (a *App) HistorySliceForTarget(selector SessionSelector, req HistorySliceRequest) (HistorySlice, error) {
	target, err := a.resolveSessionTargetWithArchived(selector, true)
	if err != nil {
		return emptyHistorySlice(), err
	}
	targetKey := target.key()
	req.Cursor, err = decodeTargetHistorySliceCursor(req.Cursor, targetKey)
	if err != nil {
		return emptyHistorySlice(), err
	}
	req = normalizeHistorySliceRequest(req)
	if target.SessionRef.SessionID != "" {
		page, err := a.canonicalHistorySlice(a.desktopSessionService("").Query(), target.SessionRef, "", "", req)
		page.NextCursor = encodeTargetHistorySliceCursor(targetKey, page.NextCursor)
		return page, err
	}
	dir, path, err := a.sessionDirForPath(target.SessionPath)
	if err != nil {
		return emptyHistorySlice(), newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	page, err := a.coldHistorySlice(dir, path, req)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "cursor") {
			return emptyHistorySlice(), newSessionOperationError("stale_cursor", "The session content changed. Reload it and try again.")
		}
		return emptyHistorySlice(), err
	}
	page.NextCursor = encodeTargetHistorySliceCursor(targetKey, page.NextCursor)
	return page, nil
}

// SearchHistoryContentForTarget searches the disposable legacy history index
// for one exact session. The cursor is bound to target, query, and index
// revision, so changing tabs or replaying a cursor against a sibling cannot
// change the routed object.
func (a *App) SearchHistoryContentForTarget(selector SessionSelector, query, cursor string, limit int) (HistorySearchPage, error) {
	target, err := a.resolveSessionTargetWithArchived(selector, true)
	if err != nil {
		return HistorySearchPage{Items: []HistorySearchHit{}}, err
	}
	if target.SessionRef.SessionID != "" {
		return HistorySearchPage{Items: []HistorySearchHit{}}, newSessionOperationError("unsupported", "Use canonical session search for this session.")
	}
	_, path, err := a.sessionDirForPath(target.SessionPath)
	if err != nil {
		return HistorySearchPage{Items: []HistorySearchHit{}}, newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	out := a.searchHistorySnapshot(HistorySearchRequest{Query: query, Cursor: cursor, Limit: limit}, path)
	if out.StaleCursor {
		return out, snapshotStale("snapshot_unavailable")
	}
	if out.ReadError != nil {
		return out, fmt.Errorf("%s", out.ReadError.Message)
	}
	return out, nil
}
