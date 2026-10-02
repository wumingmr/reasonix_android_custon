package main

import (
	"log/slog"
	"path/filepath"
	"strings"
)

// The tab's workspace chooses where new sessions are created, not where an
// already admitted historical source lives. Resolve compatibility reads by
// their captured path while retaining the known-root and symlink checks.
// Call outside App.mu: known roots can consult project metadata and tab state.
func (a *App) historyReadSource(sessionDir, sessionPath string) (string, string, error) {
	if path, _, err := validateSessionPath(sessionDir, sessionPath); err == nil {
		return sessionDir, path, nil
	} else if !filepath.IsAbs(sessionPath) {
		return "", "", err
	}
	return a.sessionDirForPath(sessionPath)
}

// historySliceBeforeController keeps durable history observable when model
// configuration prevents the tab's execution controller from starting.
func (a *App) historySliceBeforeController(tabID, sessionDir, sessionPath, sessionID string, req HistorySliceRequest) HistorySlice {
	if sessionID != "" {
		query, ref, err := a.canonicalSessionQuery(tabID)
		if err != nil {
			return failedHistorySlice(err.Error())
		}
		slice, err := a.canonicalHistorySlice(query, ref, sessionDir, sessionPath, req)
		if err != nil {
			slog.Debug("desktop: cold canonical history slice failed", "session", ref.SessionID, "err", err)
			return failedHistorySlice(err.Error())
		}
		return slice
	}
	if strings.TrimSpace(sessionPath) == "" {
		return failedHistorySlice("session path unavailable before controller ready")
	}
	slice, err := a.coldHistorySlice(sessionDir, sessionPath, req)
	if err != nil {
		slog.Debug("desktop: cold history slice failed", "path", sessionPath, "err", err)
		return failedHistorySlice(err.Error())
	}
	return slice
}

func (a *App) canonicalHistoryContentBeforeController(
	tabID, sessionDir, sessionPath, sessionID string,
	msgIndex, sub int,
	ref HistoryContentRef,
	chunkIndex int,
	out HistoryContentChunk,
) HistoryContentChunk {
	query, sessionRef, err := a.canonicalSessionQuery(tabID)
	if err != nil || entryIDSession(ref.EntryID) != sessionID || sessionRef.SessionID != sessionID {
		out.Stale = true
		return out
	}
	src, err := canonicalHistorySliceSource(query, sessionRef)
	if err != nil || !src.identityMatches(ref.Revision, ref.RevKnown, ref.Digest) {
		out.Stale = true
		return out
	}
	value, found, stale := a.historyFieldValueForSource(src, msgIndex, sub, ref, sessionDisplayResolver(sessionDir, sessionPath), nil, nil)
	if stale || !found || len(value) != ref.Size {
		out.Stale = true
		return out
	}
	out.Data, out.Chunks = historyContentChunkAt(value, chunkIndex)
	out.Done = chunkIndex >= out.Chunks-1
	return out
}
