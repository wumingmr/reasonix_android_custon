package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/history"
	"reasonix/internal/historycatalog"
	"reasonix/internal/retrieval"
)

type historySearchSnapshotBuild struct {
	ctx      context.Context
	app      *App
	store    *readSnapshotStore
	snapshot *readSnapshot
	catalog  *historycatalog.Catalog
	request  HistorySearchRequest
	active   string
	meta     HistorySearchPage
	fence    *readSourceFence
	overlays map[string]catalogRuntimeOverlay
	terms    []string
	cutoff   time.Time
	needed   map[string]map[historyTextKey]struct{}
	order    []string
	sources  map[string]*historySearchSource
}

// historySearchSource is one session file read once per build: the digest it
// had and the snippet of every candidate that points into it.
type historySearchSource struct {
	digest   string
	covered  bool
	intact   bool
	snippets map[historyTextKey]string
}

type historyTextKey struct {
	message, part int
	kind          string
}

var loadHistorySearchMessages = agent.LoadSessionDisplayMessages

func (a *App) buildHistorySearchSnapshot(ctx context.Context, store *readSnapshotStore, snap *readSnapshot, req HistorySearchRequest, targetPath, active string) error {
	status := a.GetHistoryIndexStatus()
	meta := HistorySearchPage{Status: status, Revision: status.Revision, Partial: status.State != "ready" || status.Pending > 0}
	catalog := history.SharedCatalog()
	if catalog == nil || req.Query == "" {
		snap.metadata, _ = json.Marshal(meta)
		return nil
	}
	candidates := &readSnapshot{}
	defer store.dispose(candidates)
	roots := historySearchRootFilter(a, req)
	if targetPath != "" {
		roots = nil
	}
	search := historycatalog.SearchRequest{Query: req.Query, Scope: req.Scope, WorkspaceRoot: req.WorkspaceRoot, SessionPath: targetPath, Kinds: req.Kinds, ToolName: req.ToolName, Roots: roots}
	if err := catalog.CaptureSearch(ctx, search, func(row historycatalog.Candidate) error { return store.append(ctx, candidates, row) }); err != nil {
		return err
	}
	terms, err := retrieval.QueryTerms(req.Query)
	if err != nil {
		return err
	}
	fence, err := a.newReadSourceFence(store, snap)
	if err != nil {
		return err
	}
	_, overlays := a.catalogRuntimeOverlays()
	build := &historySearchSnapshotBuild{ctx: ctx, app: a, store: store, snapshot: snap, catalog: catalog, request: req, active: active, meta: meta, fence: fence, overlays: overlays, terms: terms, cutoff: time.Now()}
	if err := build.run(candidates); err != nil {
		return err
	}
	snap.validate = fence.freeze()
	snap.metadata, err = json.Marshal(build.meta)
	return err
}

// run keeps the bm25 order the catalog returned while reading each session file
// once: hits interleave sessions, so loading per hit rereads a file per row.
func (b *historySearchSnapshotBuild) run(candidates *readSnapshot) error {
	b.needed, b.sources = map[string]map[historyTextKey]struct{}{}, map[string]*historySearchSource{}
	if err := candidates.walk(b.ctx, b.collect); err != nil {
		return err
	}
	for _, path := range b.order {
		if err := b.loadSource(path, b.needed[path]); err != nil {
			return err
		}
		delete(b.needed, path)
	}
	return candidates.walk(b.ctx, b.visit)
}

func (b *historySearchSnapshotBuild) candidate(encoded []byte) (historycatalog.Candidate, catalogRuntimeOverlay, bool, error) {
	if err := b.ctx.Err(); err != nil {
		return historycatalog.Candidate{}, catalogRuntimeOverlay{}, false, err
	}
	var row historycatalog.Candidate
	if err := json.Unmarshal(encoded, &row); err != nil {
		return row, catalogRuntimeOverlay{}, false, err
	}
	overlay := b.overlays[sessionRuntimeKey(row.SessionPath)]
	keep := historyStatusMatches(b.request.Status, overlay.open, row.SessionPath == b.active) && historyTimeMatchesAt(row.LastActivityAt, b.request.TimeFilter, b.cutoff)
	return row, overlay, keep, nil
}

func (b *historySearchSnapshotBuild) collect(encoded []byte) error {
	row, _, keep, err := b.candidate(encoded)
	if err != nil || !keep {
		return err
	}
	keys, ok := b.needed[row.SessionPath]
	if !ok {
		keys = map[historyTextKey]struct{}{}
		b.needed[row.SessionPath] = keys
		b.order = append(b.order, row.SessionPath)
	}
	keys[historyTextKey{message: row.MessageIndex, part: row.PartIndex, kind: row.Kind}] = struct{}{}
	return nil
}

func (b *historySearchSnapshotBuild) visit(encoded []byte) error {
	row, overlay, keep, err := b.candidate(encoded)
	if err != nil || !keep {
		return err
	}
	src := b.sources[row.SessionPath]
	if src.covered {
		return nil
	}
	if !src.intact || row.ContentDigest == "" || src.digest != row.ContentDigest {
		b.catalog.EnqueueExisting(b.ctx, row.SessionPath)
		b.meta.Partial = true
		return nil
	}
	snippet, ok := src.snippets[historyTextKey{message: row.MessageIndex, part: row.PartIndex, kind: row.Kind}]
	if !ok {
		b.catalog.EnqueueExisting(b.ctx, row.SessionPath)
		b.meta.Partial = true
		return nil
	}
	hit := HistorySearchHit{SessionPath: row.SessionPath, SessionID: strings.TrimSuffix(filepath.Base(row.SessionPath), filepath.Ext(row.SessionPath)), Source: row.Source, MessageIndex: row.MessageIndex, PartIndex: row.PartIndex, ContentDigest: src.digest, Role: row.Role, Kind: row.Kind, ToolName: row.ToolName, Snippet: snippet, Score: row.Score, SessionTitle: row.SessionTitle, TopicTitle: row.TopicTitle, WorkspaceRoot: row.WorkspaceRoot, LastActivityAt: row.LastActivityAt, Open: overlay.open, Running: overlay.running, Current: row.SessionPath == b.active}
	return b.store.append(b.ctx, b.snapshot, hit)
}

func (b *historySearchSnapshotBuild) loadSource(path string, keys map[historyTextKey]struct{}) error {
	ctx := b.ctx
	src := &historySearchSource{}
	b.sources[path] = src
	if sessions := b.app.sessionCatalog.Load(); sessions != nil {
		record, ok, err := sessions.GetSession(ctx, path)
		if err != nil {
			return err
		}
		src.covered = ok && record.RecoveryCopy
	}
	if src.covered {
		return nil
	}
	if err := b.fence.add(ctx, path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		b.meta.Partial = true
		return nil
	}
	messages, state, intact, err := loadHistorySearchMessages(path)
	if errors.Is(err, os.ErrNotExist) {
		b.meta.Partial = true
		return nil
	}
	if err != nil {
		return err
	}
	src.digest, src.intact = state.DigestHex, intact
	src.snippets = make(map[historyTextKey]string, len(keys))
	for key := range keys {
		text, ok := desktopHistoryText(messages, historycatalog.Candidate{MessageIndex: key.message, PartIndex: key.part, Kind: key.kind})
		if ok {
			src.snippets[key] = retrieval.MakeSnippet(text, b.request.Query, b.terms, 240)
		}
	}
	return nil
}
