package main

import (
	"context"
	"errors"
	"log/slog"
)

type historicalLegacyReconcileState struct {
	pending bool
	dirty   bool
}

// The metadata catalog can finish after the startup historical pass. Coalesce
// its completion events so missing legacy receipts recover on the same launch,
// without a second directory scan or work on the renderer's read path.
func (a *App) requestHistoricalLegacyReconciliation(ctx context.Context) {
	c := &a.historicalImports
	c.mu.Lock()
	if !c.catalogEnabled || c.stopped || ctx.Err() != nil || a.shuttingDown.Load() {
		c.mu.Unlock()
		return
	}
	c.legacyReconcile.dirty = true
	if c.legacyReconcile.pending {
		c.mu.Unlock()
		return
	}
	c.legacyReconcile.pending = true
	c.workers.Add(1)
	workerCtx := c.ctx
	c.mu.Unlock()
	go func() {
		defer c.workers.Done()
		for {
			c.mu.Lock()
			if c.stopped || workerCtx.Err() != nil || !c.legacyReconcile.dirty {
				c.legacyReconcile.pending = false
				c.mu.Unlock()
				return
			}
			c.legacyReconcile.dirty = false
			c.mu.Unlock()
			if err := a.reconcileDiscoveredLegacyCatalog(workerCtx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("desktop: legacy receipt recovery deferred")
			}
		}
	}()
}

func (a *App) reconcileDiscoveredLegacyCatalog(ctx context.Context) error {
	c := &a.historicalImports
	c.discoveryMu.Lock()
	defer c.discoveryMu.Unlock()
	before, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	sources := map[string]historicalSource{}
	if err := a.addHistoricalCatalogReceipts(ctx, func(path, format, scope, root, head string) {
		sources[desktopSourceKey(path, head)] = historicalSource{path: path, format: format, scope: scope, root: root, head: head}
	}); err != nil {
		return err
	}
	if err := a.reconcileHistoricalLegacyCatalog(ctx, sources); err != nil {
		return err
	}
	after, err := a.workspaceRegistry().Load(ctx)
	if err == nil && before.Generation != after.Generation {
		a.emitProjectTreeChangedEvent()
	}
	return err
}
