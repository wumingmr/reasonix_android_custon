package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"reasonix/internal/control"
	"reasonix/internal/session"
	"reasonix/internal/sessioncontent"
)

const sessionHistoryContentChunkBytes = 1 << 20

// SessionHistoryOutlineForTab reads durable navigation metadata before runtime startup.
func (a *App) SessionHistoryOutlineForTab(tabID string, req session.HistoryOutlineRequest) (session.HistoryOutlinePage, error) {
	query, ref, err := a.canonicalSessionQuery(tabID)
	if err != nil {
		return session.HistoryOutlinePage{Entries: []session.HistoryOutlineEntry{}}, err
	}
	return query.ReadHistoryOutline(context.Background(), ref, req)
}

// SessionHistoryContentChunk is one bounded binary chunk from a canonical
// content reference. Data is base64 so the desktop JSON contract never
// converts arbitrary attachment bytes through UTF-8 strings.
type SessionHistoryContentChunk struct {
	Data       string `json:"data"`
	NextOffset int64  `json:"nextOffset"`
	Done       bool   `json:"done"`
}

// SessionHistoryPageForTab is the canonical v4 history endpoint. Unlike the
// compatibility HistorySlice API, it is identity based and obtains its fixed
// snapshot directly from the shared session read model.
func (a *App) SessionHistoryPageForTab(tabID, cursor string, limit int) (session.MessageHistoryPage, error) {
	query, ref, err := a.canonicalSessionQuery(tabID)
	if err != nil {
		return session.MessageHistoryPage{}, err
	}
	return query.HistoryPage(context.Background(), ref, cursor, limit)
}

// SessionHistoryPageForTarget reads a cold or live canonical session by its
// explicit durable identity. It never stages the session into a tab.
func (a *App) SessionHistoryPageForTarget(selector SessionSelector, cursor string, limit int) (session.MessageHistoryPage, error) {
	target, err := a.resolveSessionTargetWithArchived(selector, true)
	if err != nil {
		return session.MessageHistoryPage{}, err
	}
	if target.SessionRef.SessionID == "" {
		return session.MessageHistoryPage{}, newSessionOperationError("unsupported", "This historical session uses the legacy history reader.")
	}
	return a.desktopSessionService("").Query().HistoryPage(context.Background(), target.SessionRef, cursor, limit)
}

// SessionOpenForTab returns the bounded recent baseline and independent
// preparation states without consulting either SQLite projection.
func (a *App) SessionOpenForTab(tabID string) (session.SessionOpenView, error) {
	query, ref, err := a.canonicalSessionQuery(tabID)
	if err != nil {
		return session.SessionOpenView{}, err
	}
	return query.OpenSession(context.Background(), ref)
}

// canonicalTabHistoryFingerprint returns the same identity emitted by the
// canonical history-window endpoint. Branch metadata describes the legacy
// JSONL projection and must never be compared with canonical session pages.
//
// tabMeta calls this while holding App.mu, so this helper deliberately avoids
// canonicalSessionQuery (which would reacquire App.mu).
func (a *App) canonicalTabHistoryFingerprint(tab *WorkspaceTab) (int64, string, bool) {
	if tab == nil || strings.TrimSpace(tab.SessionID) == "" {
		return 0, "", false
	}
	if identity, ok := tab.Ctrl.(control.IdentityLifecycle); ok && identity.UsesExclusiveSession() {
		if boundRef, bound := identity.SessionRef(); bound {
			if service := identity.SessionService(); service != nil {
				if runtime, live := service.Runtime(boundRef); live {
					recent := runtime.Session().RecentSnapshot()
					return int64(recent.DurableSequence), recent.StorageGeneration, recent.StorageGeneration != ""
				}
			}
		}
	}
	// Cold identities are validated by the off-lock reader. Tab metadata must
	// never open a service, read a snapshot, or probe a filesystem under App.mu.
	return 0, "", false
}

func (a *App) tabHistoryFingerprint(tab *WorkspaceTab, sessionPath string) (int64, string) {
	if revision, digest, ok := a.canonicalTabHistoryFingerprint(tab); ok {
		return revision, digest
	}
	if tab != nil && strings.TrimSpace(tab.SessionID) == "" {
		if ctrl, ok := tab.Ctrl.(historyWindowController); ok {
			if state, known := ctrl.SessionPersistedState(); known {
				return state.Revision, state.DigestHex
			}
		}
	}
	return 0, ""
}

func (a *App) SearchSessionHistoryForTab(tabID, textQuery, cursor string, limit int) (session.SearchHistoryPage, error) {
	query, ref, err := a.canonicalSessionQuery(tabID)
	if err != nil {
		return session.SearchHistoryPage{}, err
	}
	return query.SearchHistory(context.Background(), ref, textQuery, cursor, limit)
}

// SearchSessionHistoryForTarget searches one explicit canonical session
// without consulting the active tab.
func (a *App) SearchSessionHistoryForTarget(selector SessionSelector, textQuery, cursor string, limit int) (session.SearchHistoryPage, error) {
	target, err := a.resolveSessionTargetWithArchived(selector, true)
	if err != nil {
		return session.SearchHistoryPage{}, err
	}
	if target.SessionRef.SessionID == "" {
		return session.SearchHistoryPage{}, newSessionOperationError("unsupported", "This historical session uses the legacy search index.")
	}
	return a.desktopSessionService("").Query().SearchHistory(context.Background(), target.SessionRef, textQuery, cursor, limit)
}

func (a *App) LocateSessionMessageForTab(tabID, messageID string, snapshot uint64) (session.MessageLocation, error) {
	query, ref, err := a.canonicalSessionQuery(tabID)
	if err != nil {
		return session.MessageLocation{}, err
	}
	return query.LocateMessage(context.Background(), ref, messageID, snapshot)
}

// LocateSessionMessageForTarget resolves one canonical message against the
// explicit durable target. The active tab is deliberately irrelevant.
func (a *App) LocateSessionMessageForTarget(selector SessionSelector, messageID string, snapshot uint64) (session.MessageLocation, error) {
	target, err := a.resolveSessionTargetWithArchived(selector, true)
	if err != nil {
		return session.MessageLocation{}, err
	}
	if target.SessionRef.SessionID == "" {
		return session.MessageLocation{}, newSessionOperationError("unsupported", "This historical session uses the legacy history reader.")
	}
	return a.desktopSessionService("").Query().LocateMessage(context.Background(), target.SessionRef, messageID, snapshot)
}

// SessionHistoryContentForTab reads the next bounded chunk only after Query
// proves that the reference belongs to this session's durable view.
func (a *App) SessionHistoryContentForTab(tabID string, ref sessioncontent.Ref, offset int64) (SessionHistoryContentChunk, error) {
	query, sessionRef, err := a.canonicalSessionQuery(tabID)
	if err != nil {
		return SessionHistoryContentChunk{}, err
	}
	return readSessionHistoryContent(query, sessionRef, ref, offset)
}

// SessionHistoryContentForTarget reads a content capability against one
// explicit canonical target without opening or selecting it.
func (a *App) SessionHistoryContentForTarget(selector SessionSelector, ref sessioncontent.Ref, offset int64) (SessionHistoryContentChunk, error) {
	target, err := a.resolveSessionTargetWithArchived(selector, true)
	if err != nil {
		return SessionHistoryContentChunk{}, err
	}
	if target.SessionRef.SessionID == "" {
		return SessionHistoryContentChunk{}, newSessionOperationError("unsupported", "This historical session uses the legacy history reader.")
	}
	return readSessionHistoryContent(a.desktopSessionService("").Query(), target.SessionRef, ref, offset)
}

func readSessionHistoryContent(query *session.Query, sessionRef session.SessionRef, ref sessioncontent.Ref, offset int64) (SessionHistoryContentChunk, error) {
	if offset < 0 || offset > ref.Bytes {
		return SessionHistoryContentChunk{}, errors.New("invalid session history content offset")
	}
	if offset == ref.Bytes {
		return SessionHistoryContentChunk{NextOffset: offset, Done: true}, nil
	}
	length := min(int64(sessionHistoryContentChunkBytes), ref.Bytes-offset)
	data, err := query.ReadContent(context.Background(), sessionRef, ref, offset, length)
	if err != nil {
		return SessionHistoryContentChunk{}, err
	}
	next := offset + int64(len(data))
	return SessionHistoryContentChunk{Data: base64.StdEncoding.EncodeToString(data), NextOffset: next, Done: next == ref.Bytes}, nil
}

// SessionHistoryWindowForTab pages a bounded window around an anchor
// (newest/message/turn/cursor) in either direction — the history-window-v1
// capability. Anchors resolve through the locator index without walking pages.
func (a *App) SessionHistoryWindowForTab(tabID string, req session.HistoryWindowRequest) (session.HistoryWindowPage, error) {
	query, ref, err := a.canonicalSessionQuery(tabID)
	if err != nil {
		return session.HistoryWindowPage{}, err
	}
	return query.ReadHistoryWindow(context.Background(), ref, req)
}

// SessionHistoryWindowForTarget resolves a fixed-snapshot window for one
// explicit canonical target and does not alter the visible transcript.
func (a *App) SessionHistoryWindowForTarget(selector SessionSelector, req session.HistoryWindowRequest) (session.HistoryWindowPage, error) {
	target, err := a.resolveSessionTargetWithArchived(selector, true)
	if err != nil {
		return session.HistoryWindowPage{}, err
	}
	if target.SessionRef.SessionID == "" {
		return session.HistoryWindowPage{}, newSessionOperationError("unsupported", "This historical session uses the legacy history reader.")
	}
	return a.desktopSessionService("").Query().ReadHistoryWindow(context.Background(), target.SessionRef, req)
}

// SessionMessageFieldForTab returns one bounded fragment of one top-level
// message field. Credentials issued when a window or page displayed the
// message authorize the read.
func (a *App) SessionMessageFieldForTab(tabID, messageID string, version int, field string, offset, length int64) (session.MessageFieldPage, error) {
	query, ref, err := a.canonicalSessionQuery(tabID)
	if err != nil {
		return session.MessageFieldPage{}, err
	}
	return query.ReadMessageField(context.Background(), ref, messageID, version, field, offset, length)
}

// SessionMessageFieldForTarget reads one bounded field fragment from an
// explicit canonical target without consulting the active runtime.
func (a *App) SessionMessageFieldForTarget(selector SessionSelector, messageID string, version int, field string, offset, length int64) (session.MessageFieldPage, error) {
	target, err := a.resolveSessionTargetWithArchived(selector, true)
	if err != nil {
		return session.MessageFieldPage{}, err
	}
	if target.SessionRef.SessionID == "" {
		return session.MessageFieldPage{}, newSessionOperationError("unsupported", "This historical session uses the legacy history reader.")
	}
	return a.desktopSessionService("").Query().ReadMessageField(
		context.Background(),
		target.SessionRef,
		messageID,
		version,
		field,
		offset,
		length,
	)
}

func (a *App) canonicalSessionQuery(tabID string) (*session.Query, session.SessionRef, error) {
	a.mu.RLock()
	tab := a.tabByIDLocked(tabID)
	var ctrl control.SessionAPI
	var sessionID, sessionDir string
	if tab != nil {
		ctrl = tab.Ctrl
		sessionID = tab.SessionID
		sessionDir = tabSessionDir(tab)
	}
	a.mu.RUnlock()
	if ctrl == nil {
		if tab == nil {
			return nil, session.SessionRef{}, fmt.Errorf("tab %q is not ready", tabID)
		}
		if sessionID == "" {
			return nil, session.SessionRef{}, errors.New("canonical session identity is unavailable")
		}
		service := a.desktopSessionService(sessionDir)
		if service == nil || service.Query() == nil {
			return nil, session.SessionRef{}, errors.New("canonical session history is unavailable")
		}
		return service.Query(), session.SessionRef{HostID: service.HostID(), SessionID: sessionID}, nil
	}
	identity, ok := ctrl.(control.IdentityLifecycle)
	if !ok || !identity.UsesExclusiveSession() {
		return nil, session.SessionRef{}, errors.New("canonical session history is unavailable")
	}
	ref, bound := identity.SessionRef()
	service := identity.SessionService()
	if !bound || service == nil || service.Query() == nil {
		return nil, session.SessionRef{}, errors.New("canonical session identity is unavailable")
	}
	return service.Query(), ref, nil
}
