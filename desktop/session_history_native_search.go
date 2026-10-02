package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"reasonix/internal/config"
	"reasonix/internal/projectiondb"
	"reasonix/internal/session"
)

// The native preparation owns both databases. Search is admitted only after
// an explicit request and joins that owner's retirement barrier before close.
type nativeHistorySearch struct {
	done chan struct{}
	db   *sql.DB
	err  error
}

func (p *nativeHistoryPreparation) closeSearch() {
	p.searchMu.Lock()
	search := p.search
	p.searchMu.Unlock()
	if search != nil {
		<-search.done
		if search.db != nil {
			_ = search.db.Close()
		}
	}
}

func (a *App) prepareNativeHistorySearch(p *nativeHistoryPreparation) *nativeHistorySearch {
	p.searchMu.Lock()
	defer p.searchMu.Unlock()
	if p.search != nil {
		return p.search
	}
	search := &nativeHistorySearch{done: make(chan struct{})}
	if err := p.ctx.Err(); err != nil {
		search.err = err
		close(search.done)
		return search
	}
	p.search = search
	go func() {
		defer close(search.done)
		release, err := a.historyMaintenance.Foreground(p.ctx)
		if err != nil {
			search.err = err
			return
		}
		defer release()
		search.db, search.err = openNativeSearch(p)
	}()
	return search
}

type nativeSearchCursor struct {
	Version    int    `json:"v"`
	Generation string `json:"g"`
	Query      string `json:"q"`
	Before     int64  `json:"b"`
}

func (a *App) searchNativeHistory(r *desktopHistoryReader, text, cursor string, limit int) (session.SearchHistoryPage, error) {
	page := session.SearchHistoryPage{Hits: []session.SearchHistoryHit{}, Status: "failed"}
	text = strings.TrimSpace(text)
	if text == "" {
		return page, errors.New("history search query is required")
	}
	p, err := nativeNavigationPager(r)
	if err != nil {
		page.Status = nativeNavigationStatus(r, err)
		if page.Status != "failed" {
			err = nil
		}
		return page, err
	}
	page.SnapshotSequence, page.CoverageSequence = nativeHistorySequence(p), nativeHistorySequence(p)
	before := int64(p.Header.MessageCount)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	if cursor != "" {
		var cut nativeSearchCursor
		body, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(body, &cut) != nil || cut.Version != 1 || cut.Generation != r.native.key || cut.Query != digest || cut.Before <= 0 || cut.Before > before {
			page.Status = "stale_cursor"
			return page, nil
		}
		before = cut.Before
	}
	search := a.prepareNativeHistorySearch(r.native)
	select {
	case <-search.done:
		if search.err != nil {
			return page, search.err
		}
	default:
		page.Status, page.CoverageSequence = "preparing", 0
		return page, nil
	}
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	var rows *sql.Rows
	if utf8.RuneCountInString(text) >= 3 {
		match := `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
		rows, err = search.db.QueryContext(r.ctx, `SELECT d.position,d.role,d.preview FROM documents_fts JOIN documents d ON d.position=documents_fts.rowid WHERE documents_fts MATCH ? AND instr(d.text,?)>0 AND d.position<? ORDER BY d.position DESC LIMIT ?`, match, text, before, limit+1)
	} else {
		rows, err = search.db.QueryContext(r.ctx, `SELECT position,role,preview FROM documents WHERE instr(text,?)>0 AND position<? ORDER BY position DESC LIMIT ?`, text, before, limit+1)
	}
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var hit session.SearchHistoryHit
		if err := rows.Scan(&hit.Position, &hit.Role, &hit.Preview); err != nil {
			return page, err
		}
		if len(page.Hits) == limit {
			page.HasMore = true
			break
		}
		hit.MessageID, hit.EventSequence = nativeHistoryEntryID(r, p, int(hit.Position)), page.SnapshotSequence
		page.Hits = append(page.Hits, hit)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if err := p.Validate(); err != nil {
		return page, err
	}
	if page.HasMore {
		body, err := json.Marshal(nativeSearchCursor{Version: 1, Generation: r.native.key, Query: digest, Before: page.Hits[len(page.Hits)-1].Position})
		if err != nil {
			return page, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(body)
	}
	page.Status = "ready"
	return page, nil
}

var nativeSearchMigrations = []projectiondb.Migration{{Version: 1, Apply: func(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
	CREATE TABLE documents(position INTEGER PRIMARY KEY,role TEXT NOT NULL,preview TEXT NOT NULL,text TEXT NOT NULL);
	CREATE VIRTUAL TABLE documents_fts USING fts5(text,content='documents',content_rowid='position',tokenize='trigram');`)
	return err
}}}

func openNativeSearch(p *nativeHistoryPreparation) (*sql.DB, error) {
	root := config.CacheDir()
	if root == "" {
		return nil, errors.New("history cache unavailable")
	}
	// This versioned cache is disposable and never writes into the source's
	// session directory. Older readers can ignore it without a migration.
	opts := projectiondb.OpenOptions{Path: filepath.Join(root, "history-search-v1", p.cacheKey+".sqlite"), Migrations: nativeSearchMigrations, RequireDisk: true, MaxOpenConns: 1}
	if handle, err := projectiondb.Open(p.ctx, opts); err == nil {
		var generation string
		err = handle.DB.QueryRowContext(p.ctx, `SELECT value FROM metadata WHERE key='complete'`).Scan(&generation)
		if err == nil && generation == p.key {
			if err := p.pager.Validate(); err == nil {
				return handle.DB, nil
			}
		}
		_ = handle.DB.Close()
	}
	if err := projectiondb.Rebuild(p.ctx, opts, func(ctx context.Context, db *sql.DB) error {
		return buildNativeSearch(ctx, db, p)
	}); err != nil {
		return nil, err
	}
	handle, err := projectiondb.Open(p.ctx, opts)
	if err != nil {
		return nil, err
	}
	return handle.DB, nil
}
