package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/historywork"
	"reasonix/internal/session"
)

type historicalCatalogEntry struct {
	scope         string
	node          ProjectNode
	sourceChanged bool
}

// Discovery repairs incomplete adoption before publishing metadata. Ordinary
// pagination never visits source directories or replays a historical log.
func (a *App) requestHistoricalCatalog() {
	a.requestHistoricalCatalogWithContext(a.bootContext())
}

// requestHistoricalCatalogWithContext lets lifecycle owners pass the context
// that already governs their worker. Background catalog callbacks must not
// reread App.ctx while tests or the shell are replacing that field.
func (a *App) requestHistoricalCatalogWithContext(baseCtx context.Context) {
	c := &a.historicalImports
	c.mu.Lock()
	c.initialize(baseCtx)
	if !c.catalogEnabled || c.stopped || a.shuttingDown.Load() || c.discoveryPending || time.Since(c.catalogAt) < 5*time.Minute {
		c.mu.Unlock()
		return
	}
	c.discoveryPending = true
	ctx := c.ctx
	c.workers.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.workers.Done()
		defer func() {
			c.mu.Lock()
			c.discoveryPending = false
			c.mu.Unlock()
		}()
		if _, err := a.discoverHistoricalSessions(ctx, false); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("desktop: historical catalog discovery incomplete")
		}
	}()
}

func (a *App) listHistoricalSessions(ctx context.Context) (HistoricalImportStatus, error) {
	return a.discoverHistoricalSessions(ctx, true)
}

func (a *App) discoverHistoricalSessions(ctx context.Context, includeLegacy bool) (HistoricalImportStatus, error) {
	c := &a.historicalImports
	c.discoveryMu.Lock()
	defer c.discoveryMu.Unlock()
	// Failed reads also settle the refresh interval. Otherwise a renderer read
	// can immediately re-admit the same failed discovery.
	defer c.finishCatalogRefresh()
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return HistoricalImportStatus{Items: []HistoricalSessionView{}}, err
	}
	c.mu.Lock()
	positions := make(map[string]int, len(c.catalog))
	for i, entry := range c.catalog {
		positions[entry.node.Key] = i
	}
	c.mu.Unlock()
	publish := a.historicalCatalogPublisher(ctx, positions)
	sources := map[string]historicalSource{}
	add := func(path, format, scope, root, head string) {
		key := desktopSourceKey(path, head)
		sources[key] = historicalSource{path: path, format: format, scope: scope, root: root, head: head}
	}
	canonical, legacy := a.desktopHistoricalRoots()
	var joined error
	for _, source := range canonical {
		joined = errors.Join(joined, scanHistoricalRoot(ctx, *source, "canonical", add, &a.historyMaintenance))
	}
	// Ordinary legacy discovery belongs to sessioncatalog. Only the explicit
	// management listing enumerates it here to preserve that API's semantics.
	if includeLegacy {
		for _, source := range legacy {
			joined = errors.Join(joined, scanHistoricalRoot(ctx, source, "legacy", add, &a.historyMaintenance))
		}
	}
	addHistoricalRegistrySources(state, add)
	joined = errors.Join(joined, a.addHistoricalCatalogReceipts(ctx, add))
	joined = errors.Join(joined, addHistoricalLegacyReceiptHeads(sources, add))
	if err := a.reconcileHistoricalLegacyCatalog(ctx, sources); err != nil {
		return HistoricalImportStatus{Items: []HistoricalSessionView{}}, err
	}
	publishRecovered := func(batch []historicalCatalogEntry) {
		if err := a.reconcileHistoricalCatalog(ctx, batch, sources); err == nil {
			publish(batch)
		}
	}
	catalog := readHistoricalCanonicalCatalog(ctx, sources, &a.historyMaintenance, publishRecovered, state)
	if err := a.reconcileHistoricalCatalog(ctx, catalog, sources); err != nil {
		return HistoricalImportStatus{Items: []HistoricalSessionView{}}, err
	}
	state, err = a.workspaceRegistry().Load(ctx)
	if err != nil {
		return HistoricalImportStatus{Items: []HistoricalSessionView{}}, err
	}
	saved, presentationErr := readHistoricalSidecar()
	if err := ctx.Err(); err != nil {
		return HistoricalImportStatus{Items: []HistoricalSessionView{}}, err
	}
	c.mu.Lock()
	notify := false
	defer func() {
		c.mu.Unlock()
		if notify {
			a.emitProjectTreeChangedEvent()
		}
	}()
	if c.stopped || ctx.Err() != nil {
		return HistoricalImportStatus{Items: []HistoricalSessionView{}}, context.Canceled
	}
	c.initialize(ctx)
	if !c.queueLoaded {
		c.loadQueueLocked()
	}
	// An interrupted or inaccessible root is not evidence of deletion. Keep
	// its previously published rows until a complete discovery can prove absence.
	if joined != nil {
		catalog = c.catalog
	}
	changed := !reflect.DeepEqual(c.catalog, catalog)
	if changed {
		c.catalog = catalog
	}
	if presentationErr == nil {
		changed = changed || !reflect.DeepEqual(c.presentations, saved.Presentations)
		c.presentations = saved.Presentations
	}
	for id, source := range sources {
		if source.path == "" {
			continue
		}
		c.sources[id] = source
		view := historicalImportView(state, id, source, c.views[id])
		if presentation := c.presentations[id]; presentation.Title != "" {
			view.Title = presentation.Title
		}
		changed = changed || !reflect.DeepEqual(c.views[id], view)
		c.views[id] = view
	}
	if changed {
		c.catalogRevision++
		notify = !c.stopped
	}
	return c.status(), joined
}

func historicalCatalogPlaceholder(key string, source historicalSource) historicalCatalogEntry {
	kind := "global_topic"
	if source.scope == "project" {
		kind = "topic"
	}
	return historicalCatalogEntry{scope: source.scope, node: ProjectNode{
		Key: "source_" + key, Kind: kind, Root: source.root, Label: filepath.Base(source.path),
		TopicID: "historical-" + key, Historical: true, SessionPath: source.path, SortOrder: -1,
		TurnsState: "unknown", Health: "metadata_pending", Children: []ProjectNode{},
		Source: &SessionSourceRef{HostID: localDesktopHostID, SourceKey: key, Path: source.path},
	}}
}

func readHistoricalCanonicalCatalog(ctx context.Context, sources map[string]historicalSource, maintenance *historywork.Coordinator, publish func([]historicalCatalogEntry), states ...workspacestate.State) []historicalCatalogEntry {
	rows := []historicalCatalogEntry{}
	ledger, ledgerErr := readDesktopMigrationLedger()
	batchStart, count, bytes := 0, 0, int64(0)
	var release func(int64)
	var started time.Time
	flush := func() {
		if release != nil {
			release(bytes)
			release = nil
		}
		if publish != nil && batchStart < len(rows) {
			publish(rows[batchStart:])
		}
		batchStart, count, bytes = len(rows), 0, 0
	}
	defer func() {
		if release != nil {
			release(bytes)
		}
	}()
	ctx = maintenance.Context(ctx)
	for key, source := range sources {
		if ctx.Err() != nil {
			break
		}
		if source.format != "canonical" || source.version != "" {
			continue
		}
		// Stat reads at most three small metadata files. Charge their upper
		// bound, including sentinel bytes used to detect oversized sidecars.
		const metadataBytes = 3 * (historywork.ReadChunk + 1)
		if release != nil && (count >= historywork.BatchEntries || bytes+metadataBytes > historywork.BatchBytes || time.Since(started) >= historywork.SliceDuration) {
			flush()
		}
		if release == nil {
			var err error
			release, err = maintenance.BackgroundSlice(ctx, false)
			if err != nil {
				break
			}
			started = time.Now()
		}
		count++
		bytes += metadataBytes
		node := historicalCatalogPlaceholder(key, source).node
		if info, err := session.NewFilesystemPersistence(filepath.Dir(source.path)).Stat(ctx, filepath.Base(source.path)); err == nil {
			if info.Kind == session.SessionKindHeadlessRun {
				delete(sources, key)
				continue
			}
			if info.Title != "" {
				node.Label = info.Title
			}
			node.Preview, node.Turns = info.Preview, info.Turns
			node.CreatedAt, node.LastActivityAt = info.CreatedAt.UnixMilli(), info.UpdatedAt.UnixMilli()
			if info.MetadataStatus == session.MetadataReady {
				node.TurnsState, node.Health = "valid", "ok"
			}
		} else if stat, statErr := os.Stat(source.path); statErr == nil {
			node.CreatedAt, node.LastActivityAt = stat.ModTime().UnixMilli(), stat.ModTime().UnixMilli()
			node.Health = "degraded"
		}
		changed := false
		if len(states) > 0 && ledgerErr == nil {
			changed = historicalCanonicalSourceChanged(states[0], ledger, key, source.path)
		}
		rows = append(rows, historicalCatalogEntry{scope: source.scope, node: node, sourceChanged: changed})
	}
	// The final batch is published with completion metadata by the caller.
	// Intermediate budget boundaries publish independently for large roots.
	sort.Slice(rows, func(i, j int) bool { return rows[i].node.Key < rows[j].node.Key })
	return rows
}

func historicalCanonicalSourceChanged(state workspacestate.State, ledger desktopMigrationLedger, key, path string) bool {
	// Registry receipts outlive purged source directories. Missing files
	// are not a new source revision and must not recreate a sidebar row.
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	if _, mapped, err := historicalMappingForSource(state, key); !mapped || err != nil {
		return false
	}
	baseKey := desktopCanonicalMigrationKey(filepath.Dir(path), filepath.Base(path))
	revision, err := desktopMigrationSourceRevision(canonicalMigrationSourceFiles(filepath.Dir(path), filepath.Base(path)))
	if err != nil {
		return false
	}
	known := false
	for receiptKey, receipt := range ledger.Records {
		if !historicalSourceKeyMatches(receiptKey, baseKey) || receipt.Status != "completed" {
			continue
		}
		known = true
		if receipt.SourceRevision == revision {
			return false
		}
	}
	return known
}

func (a *App) historicalCanonicalTopics(scope, root string, state workspacestate.State) []ProjectNode {
	index := workspacestate.NewWorkspaceIndex(state)
	return a.historicalCanonicalTopicsFromProjection(scope, root, state, index)
}

func (a *App) historicalCanonicalTopicsFromProjection(scope, root string, state workspacestate.State, index *workspacestate.WorkspaceIndex) []ProjectNode {
	a.requestHistoricalCatalog()
	c := &a.historicalImports
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := []ProjectNode{}
	for _, entry := range c.catalog {
		if entry.scope != scope {
			continue
		}
		if scope == "project" && !index.SameRoot(entry.node.Root, root) {
			continue
		}
		node := entry.node
		if node.Health == "unavailable" {
			continue
		}
		if _, adopted, err := historicalMappingForSource(state, node.Source.SourceKey); adopted || err != nil {
			continue
		}
		node.PreparationStatus = "available"
		if view, ok := c.views[node.Source.SourceKey]; ok {
			node.PreparationStatus = view.Status
		}
		rows = append(rows, node)
	}
	return rows
}

func applyHistoricalPresentations(nodes []ProjectNode, saved historicalImportQueueSidecar) {
	for i := range nodes {
		if nodes[i].Source == nil {
			continue
		}
		presentation := saved.Presentations[nodes[i].Source.SourceKey]
		if presentation.Title != "" {
			nodes[i].Label = presentation.Title
		}
		if presentation.Pinned != nil {
			nodes[i].Pinned = *presentation.Pinned
		}
	}
}

// A shell-only read must not create workspaces or migrate organization state.
// Sources without canonical members still need their persisted pin overlays.
func (a *App) historicalPinnedShellsFromProjection(req ProjectTopicPageRequest, state workspacestate.State, index *workspacestate.WorkspaceIndex, legacy desktopProject) ([]ProjectNode, error) {
	workspaceID, _, _ := index.Resolve(req.WorkspaceRoot)
	if req.Scope != "project" {
		workspaceID = workspacestate.GlobalWorkspaceID
	}
	adopted := map[string]bool{}
	for _, mapping := range state.SourceMappings {
		for _, key := range state.SourceKeys(mapping.SourceKey) {
			adopted["source\x00local\x00"+key] = true
		}
		if sourceMappingHasPathAlias(mapping) {
			adopted[sessionRuntimeKey(mapping.Path)] = true
		}
	}
	page, err := a.unadoptedLegacyTopics(req, adopted, state.AdoptedTopicIDs(workspaceID))
	if err != nil {
		return nil, err
	}
	nodes := append(page.Items, a.historicalCanonicalTopicsFromProjection(req.Scope, req.WorkspaceRoot, state, index)...)
	if saved, err := readHistoricalSidecar(); err == nil {
		applyHistoricalPresentations(nodes, saved)
	}
	workspace := state.Workspaces[workspaceID]
	org := projectedShellOrganization(workspace, state, nodes, legacy)
	req.pinnedOnly = true
	pins := filterWorkspaceSessionNodes(req, org, state, workspaceID, nodes)
	sort.SliceStable(pins, func(i, j int) bool { return projectTopicLess(pins[i], pins[j], req.SortMode, org.ManualOrderEnabled) })
	return pins, nil
}

func (c *historicalImportCoordinator) finishCatalogRefresh() {
	c.mu.Lock()
	c.catalogAt = time.Now()
	c.mu.Unlock()
}
