package main

import (
	"fmt"
	"net/url"
	"reasonix/internal/servecontract"
	"reasonix/internal/session"
)

// RemoteSessionHistoryOutlineForTab never substitutes another host's history.
func (a *App) RemoteSessionHistoryOutlineForTab(tabID string, req session.HistoryOutlineRequest) (session.HistoryOutlinePage, error) {
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[tabID]
	supported := tab != nil && tab.capabilities[servecontract.HistoryOutlineV1]
	a.remoteTabMu.Unlock()
	page := session.HistoryOutlinePage{Entries: []session.HistoryOutlineEntry{}, Status: "unsupported"}
	if !supported {
		return page, nil
	}
	query := make(url.Values)
	query.Set("startTurn", fmt.Sprint(req.StartTurn))
	query.Set("limit", fmt.Sprint(req.Limit))
	query.Set("generation", req.Generation)
	if req.SnapshotSequence != nil {
		query.Set("snapshotSequence", fmt.Sprint(*req.SnapshotSequence))
	}
	ok, err := a.remoteSessionHistoryRead(tabID, "/session-history/outline", query, &page, session.HistoryPageMaxBytes)
	if err == nil && !ok {
		page.Status = "unsupported"
	}
	if page.Entries == nil {
		page.Entries = []session.HistoryOutlineEntry{}
	}
	return page, err
}
