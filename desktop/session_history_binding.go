package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"reasonix/internal/agent"
	"reasonix/internal/session"
)

type SessionHistoryReadHandle struct {
	ID                string   `json:"id"`
	StorageBackend    string   `json:"storageBackend"`
	SessionGeneration uint64   `json:"sessionGeneration"`
	Capabilities      []string `json:"capabilities"`
}

type desktopHistoryReader struct {
	handle            SessionHistoryReadHandle
	ctx               context.Context
	release           func()
	query             *session.Query
	ref               session.SessionRef
	tab               *WorkspaceTab
	tabID, path, head string
	identity          string
	native            *nativeHistoryPreparation
}

type desktopHistoryReaders struct {
	mu      sync.Mutex
	entries map[string]*desktopHistoryReader
	native  map[string]*nativeHistoryPreparation
	workers sync.WaitGroup
	closed  bool
}

// BeginSessionHistoryReadForTab captures the source once. Subsequent reads
// never resolve whichever session happens to occupy the tab at completion.
func (a *App) BeginSessionHistoryReadForTab(tabID string) (SessionHistoryReadHandle, error) {
	a.mu.RLock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil {
		a.mu.RUnlock()
		return SessionHistoryReadHandle{Capabilities: []string{}}, errors.New("history tab unavailable")
	}
	path, head, id, generation := tab.currentSessionPath(), tab.SessionHeadID, tab.SessionID, tab.SessionGeneration
	a.mu.RUnlock()
	reader := &desktopHistoryReader{tab: tab, tabID: tabID, path: path, head: head, identity: id,
		handle: SessionHistoryReadHandle{ID: newSessionRuntimeID("history"), StorageBackend: "legacy", SessionGeneration: generation, Capabilities: []string{"history-read-binding-v1"}}}
	ctx, cancel := context.WithCancel(a.bootContext())
	reader.ctx, reader.release = ctx, cancel
	if id != "" || nativeStoredDirectory(path) {
		var query *session.Query
		var ref session.SessionRef
		var err error
		if id != "" {
			query, ref, err = a.canonicalSessionQuery(tabID)
		} else {
			// Prototype/v3/v4 directories retain their original store. A cold
			// query needs neither import nor a leased execution runtime.
			var service *session.Service
			service, err = a.historicalSessionService(filepath.Dir(path))
			if err == nil {
				query = service.Query()
				ref = session.SessionRef{HostID: localDesktopHostID, SessionID: filepath.Base(path)}
			}
		}
		if err != nil {
			cancel()
			return SessionHistoryReadHandle{Capabilities: []string{}}, err
		}
		shared, release, err := query.AcquireHistoryReader(ref)
		if err != nil {
			cancel()
			return SessionHistoryReadHandle{Capabilities: []string{}}, err
		}
		cancel()
		reader.ctx, cancel = context.WithCancel(shared)
		reader.release = func() { cancel(); release() }
		reader.query, reader.ref, reader.handle.StorageBackend = query, ref, "canonical"
	}
	var nativeGeneration, nativeSourceKey string
	if reader.query == nil && path != "" {
		var err error
		nativeGeneration, err = nativeHistorySourceGeneration(path)
		if err != nil {
			reader.release()
			return SessionHistoryReadHandle{Capabilities: []string{}}, err
		}
		nativeSourceKey = sessionRuntimeKey(path)
	}
	if !a.historyReaderCurrent(reader) {
		reader.release()
		return SessionHistoryReadHandle{Capabilities: []string{}}, errors.New("history source changed")
	}
	a.historyReaders.mu.Lock()
	if a.historyReaders.entries == nil {
		a.historyReaders.entries = make(map[string]*desktopHistoryReader)
	}
	if a.shuttingDown.Load() || a.historyReaders.closed {
		a.historyReaders.mu.Unlock()
		reader.release()
		return SessionHistoryReadHandle{Capabilities: []string{}}, context.Canceled
	}
	a.historyReaders.entries[reader.handle.ID] = reader
	if reader.query == nil && reader.path != "" {
		reader.handle.Capabilities = append(reader.handle.Capabilities, "history-native-navigation-v1", "history-native-search-v1")
		var releaseNative context.CancelFunc
		reader.native, releaseNative = a.acquireNativeHistoryLocked(reader.path, reader.head, nativeSourceKey, nativeGeneration)
		release := reader.release
		// Source retirement cancels every read in that generation. Individual
		// navigation cancellation still releases only this reader's reference.
		reader.ctx, cancel = context.WithCancel(reader.native.ctx)
		reader.release = func() { cancel(); release(); releaseNative() }
	}
	a.historyReaders.mu.Unlock()
	return reader.handle, nil
}

func (a *App) historyReaderCurrent(reader *desktopHistoryReader) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	tab := a.tabByIDLocked(reader.tabID)
	return tab == reader.tab && tab.SessionGeneration == reader.handle.SessionGeneration && tab.SessionID == reader.identity && tab.currentSessionPath() == reader.path && tab.SessionHeadID == reader.head
}

func (a *App) historyReader(id string) (*desktopHistoryReader, error) {
	a.historyReaders.mu.Lock()
	reader := a.historyReaders.entries[id]
	a.historyReaders.mu.Unlock()
	if reader == nil {
		return nil, context.Canceled
	}
	if err := reader.ctx.Err(); err != nil {
		return nil, err
	}
	if !a.historyReaderCurrent(reader) {
		a.ReleaseSessionHistoryRead(id)
		return nil, context.Canceled
	}
	return reader, nil
}

// Release is idempotent and affects only the exact handle, never a new reader.
func (a *App) ReleaseSessionHistoryRead(id string) {
	a.historyReaders.mu.Lock()
	reader := a.historyReaders.entries[id]
	delete(a.historyReaders.entries, id)
	a.historyReaders.mu.Unlock()
	if reader != nil {
		reader.release()
	}
}

func (a *App) closeHistoryReaders() {
	a.historyReaders.mu.Lock()
	readers := a.historyReaders.entries
	a.historyReaders.entries = nil
	a.historyReaders.closed = true
	// Compatibility reads also borrow preparations without a persistent RPC
	// handle. Shutdown retires the owner, then joins every cache close barrier.
	for _, job := range a.historyReaders.native {
		job.cancel()
	}
	a.historyReaders.mu.Unlock()
	for _, reader := range readers {
		reader.release()
	}
	a.historyReaders.workers.Wait()
}

func (a *App) ReadSessionHistoryWindow(id string, req session.HistoryWindowRequest) (session.HistoryWindowPage, error) {
	r, err := a.historyReader(id)
	if err != nil {
		return session.HistoryWindowPage{Messages: []session.PersistentMessage{}, Status: "stale_cursor"}, nil
	}
	if r.query == nil {
		return session.HistoryWindowPage{Messages: []session.PersistentMessage{}, Status: "unsupported"}, nil
	}
	page, err := r.query.ReadHistoryWindow(r.ctx, r.ref, req)
	if r.ctx.Err() != nil || !a.historyReaderCurrent(r) {
		return session.HistoryWindowPage{Messages: []session.PersistentMessage{}, Status: "stale_cursor"}, nil
	}
	return page, err
}

type SessionHistoryReadSlice struct {
	Status string       `json:"status"`
	Page   HistorySlice `json:"page"`
}

func (a *App) ReadSessionHistorySlice(id string, req HistorySliceRequest) (SessionHistoryReadSlice, error) {
	r, err := a.historyReader(id)
	if err != nil {
		return SessionHistoryReadSlice{Status: "stale_cursor", Page: emptyHistorySlice()}, nil
	}
	if r.query != nil || r.path == "" || nativeStoredDirectory(r.path) {
		return SessionHistoryReadSlice{Status: "unsupported", Page: emptyHistorySlice()}, nil
	}
	if r.native == nil {
		return SessionHistoryReadSlice{Status: "unsupported", Page: emptyHistorySlice()}, nil
	}
	pager, err := r.native.wait(r.ctx)
	if err != nil {
		if errors.Is(err, agent.ErrDisplayFormatUnsupported) {
			return SessionHistoryReadSlice{Status: "unsupported", Page: emptyHistorySlice()}, nil
		}
		if r.ctx.Err() != nil || errors.Is(err, agent.ErrDisplaySourceChanged) {
			return SessionHistoryReadSlice{Status: "stale_cursor", Page: emptyHistorySlice()}, nil
		}
		return SessionHistoryReadSlice{Status: "failed", Page: emptyHistorySlice()}, err
	}
	req, status, err := nativeHistorySliceAnchor(r, pager, req)
	if r.ctx.Err() != nil || !a.historyReaderCurrent(r) || errors.Is(err, agent.ErrDisplaySourceChanged) {
		return SessionHistoryReadSlice{Status: "stale_cursor", Page: emptyHistorySlice()}, nil
	}
	if err != nil || status != "ready" {
		return SessionHistoryReadSlice{Status: status, Page: emptyHistorySlice()}, err
	}
	page, ready, err := a.historySliceFromPager(r.ctx, pager, filepath.Dir(r.path), r.path, normalizeHistorySliceRequest(req), r.native.key)
	if errors.Is(err, agent.ErrDisplaySourceChanged) || r.ctx.Err() != nil || !a.historyReaderCurrent(r) {
		return SessionHistoryReadSlice{Status: "stale_cursor", Page: emptyHistorySlice()}, nil
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return SessionHistoryReadSlice{Status: "stale_cursor", Page: emptyHistorySlice()}, nil
		}
		return SessionHistoryReadSlice{Status: "failed", Page: emptyHistorySlice()}, err
	}
	if !ready {
		// No preparation was admitted for this native format. Do not advertise
		// an indefinitely pending job; the negotiated compatibility reader can
		// still serve this same source until its runtime projection is bound.
		return SessionHistoryReadSlice{Status: "unsupported", Page: emptyHistorySlice()}, nil
	}
	status = "ready"
	if page.Stale {
		status = "stale_cursor"
	}
	for i := range page.Entries {
		for j := range page.Entries[i].Refs {
			page.Entries[i].Refs[j].ReadHandleID = id
		}
	}
	return SessionHistoryReadSlice{Status: status, Page: page}, nil
}

func (a *App) ReadSessionHistoryOutline(id string, req session.HistoryOutlineRequest) (session.HistoryOutlinePage, error) {
	r, err := a.historyReader(id)
	if err != nil {
		return session.HistoryOutlinePage{Entries: []session.HistoryOutlineEntry{}, Status: "stale_cursor"}, nil
	}
	var page session.HistoryOutlinePage
	if r.query == nil {
		page, err = a.readNativeHistoryOutline(r, req)
	} else {
		page, err = r.query.ReadHistoryOutline(r.ctx, r.ref, req)
	}
	if r.ctx.Err() != nil || !a.historyReaderCurrent(r) || errors.Is(err, agent.ErrDisplaySourceChanged) {
		return session.HistoryOutlinePage{Entries: []session.HistoryOutlineEntry{}, Status: "stale_cursor"}, nil
	}
	return page, err
}

func (a *App) LocateSessionHistoryMessage(id, messageID string, snapshot uint64) (session.MessageLocation, error) {
	r, err := a.historyReader(id)
	if err != nil {
		return session.MessageLocation{Status: "stale_cursor"}, nil
	}
	var location session.MessageLocation
	if r.query == nil {
		var pager *agent.DisplayPager
		pager, err = nativeNavigationPager(r)
		if err == nil {
			location, err = nativeHistoryLocation(r, pager, messageID, snapshot)
		} else {
			location.Status = nativeNavigationStatus(r, err)
			if location.Status != "failed" {
				err = nil
			}
		}
	} else {
		location, err = r.query.LocateMessage(r.ctx, r.ref, messageID, snapshot)
	}
	if r.ctx.Err() != nil || !a.historyReaderCurrent(r) || errors.Is(err, agent.ErrDisplaySourceChanged) {
		return session.MessageLocation{Status: "stale_cursor"}, nil
	}
	return location, err
}

func (a *App) SearchSessionHistoryRead(id, text, cursor string, limit int) (session.SearchHistoryPage, error) {
	r, err := a.historyReader(id)
	if err != nil {
		return session.SearchHistoryPage{Hits: []session.SearchHistoryHit{}, Status: "stale_cursor"}, nil
	}
	var page session.SearchHistoryPage
	if r.query == nil {
		page, err = a.searchNativeHistory(r, text, cursor, limit)
	} else {
		page, err = r.query.SearchHistory(r.ctx, r.ref, text, cursor, limit)
	}
	if r.ctx.Err() != nil || !a.historyReaderCurrent(r) || errors.Is(err, agent.ErrDisplaySourceChanged) {
		return session.SearchHistoryPage{Hits: []session.SearchHistoryHit{}, Status: "stale_cursor"}, nil
	}
	return page, err
}
