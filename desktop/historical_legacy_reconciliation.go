package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/historywork"
	"reasonix/internal/sessioncatalog"
	"reasonix/internal/store"
)

// Reuse catalog metadata instead of enumerating legacy directories a second
// time on startup. Only paths with migration evidence require recovery work.
func (a *App) addHistoricalCatalogReceipts(ctx context.Context, add func(string, string, string, string, string)) error {
	catalog := a.sessionCatalog.Load()
	if catalog == nil {
		return nil
	}
	ledger, err := readDesktopMigrationLedger()
	if err != nil || len(ledger.Records) == 0 {
		return err
	}
	receipts := historicalCompletedReceiptKeys(ledger)
	return catalog.WithReadView(ctx, func(ctx context.Context) error {
		req := sessioncatalog.SessionPageRequest{Scope: "all", Limit: 200}
		for {
			page, err := catalog.ListSessions(ctx, req)
			if err != nil {
				return err
			}
			for _, record := range page.Items {
				if historicalLegacyHasReceipt(receipts, record.Path, "") {
					add(record.Path, "legacy", record.Scope, record.WorkspaceRoot, "")
				}
				heads, err := catalog.ListHeads(ctx, record.Path)
				if err != nil {
					return err
				}
				for _, head := range heads {
					if historicalLegacyHasReceipt(receipts, record.Path, head.ID) {
						add(record.Path, "legacy", record.Scope, record.WorkspaceRoot, head.ID)
					}
				}
			}
			if page.NextCursor == "" || page.StaleCursor {
				return nil
			}
			req.Cursor = page.NextCursor
		}
	})
}

// Index completion once per discovery, rather than scanning the entire ledger
// for each catalog path and head. Reviewed versions retain their base identity.
func historicalCompletedReceiptKeys(ledger desktopMigrationLedger) map[string]bool {
	keys := map[string]bool{}
	for id, receipt := range ledger.Records {
		if receipt.Status == "completed" || receipt.PreviousCompletion != nil {
			key, _, _ := strings.Cut(id, ":review:")
			keys[key] = true
		}
	}
	return keys
}

func historicalLegacyHasReceipt(receipts map[string]bool, path, head string) bool {
	key := desktopLegacyMigrationKey(path)
	if head != "" {
		key = desktopLegacyHeadKey(path, head)
	}
	return receipts[key]
}

// Explicit discovery can run before the metadata catalog exists. A bounded,
// current head index supplies receipt identities without replaying the JSONL.
func addHistoricalLegacyReceiptHeads(sources map[string]historicalSource, add func(string, string, string, string, string)) error {
	ledger, err := readDesktopMigrationLedger()
	if err != nil || len(ledger.Records) == 0 {
		return err
	}
	receipts := historicalCompletedReceiptKeys(ledger)
	for _, source := range sources {
		if source.format != "legacy" || source.head != "" {
			continue
		}
		if info, err := os.Stat(store.SessionEventIndex(source.path)); err != nil || info.Size() > historywork.ReadChunk {
			continue
		}
		index, err := agent.ReadSessionHeadIndex(source.path)
		if err != nil || index == nil || !index.Current(source.path) {
			continue
		}
		for _, head := range index.Heads {
			if historicalLegacyHasReceipt(receipts, source.path, head.ID) {
				add(source.path, "legacy", source.scope, source.root, head.ID)
			}
		}
	}
	return nil
}

func (a *App) reconcileHistoricalLegacyCatalog(ctx context.Context, sources map[string]historicalSource) error {
	for key, source := range sources {
		if source.format != "legacy" {
			continue
		}
		release, err := a.historyMaintenance.BackgroundSlice(ctx, false)
		if err != nil {
			return err
		}
		err = a.reconcileHistoricalCatalogSource(ctx, key, source, "ok")
		release(historywork.ReadChunk)
		if errors.Is(err, context.Canceled) {
			return err
		}
		if historicalSourceBusyError(err) || errors.Is(err, os.ErrPermission) {
			continue
		}
		c := &a.historicalImports
		c.mu.Lock()
		c.initialize(ctx)
		c.sources[key] = source
		wasUnavailable := c.unavailableSources[key]
		if err != nil {
			c.unavailableSources[key] = true
		} else {
			delete(c.unavailableSources, key)
		}
		changed := wasUnavailable != c.unavailableSources[key]
		if changed {
			c.catalogRevision++
		}
		c.mu.Unlock()
		if changed {
			a.emitProjectTreeChangedEvent()
		}
	}
	return ctx.Err()
}

func (a *App) unavailableHistoricalSource(key string) bool {
	c := &a.historicalImports
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unavailableSources[key]
}

func (a *App) excludeUnavailableHistoricalSources(existing string) string {
	keys := []string{}
	if existing != "" {
		_ = json.Unmarshal([]byte(existing), &keys)
	}
	c := &a.historicalImports
	c.mu.Lock()
	paths := []string{}
	for key := range c.unavailableSources {
		keys = append(keys, key)
		if source, ok := c.sources[key]; ok {
			paths = append(paths, source.path)
		}
	}
	c.mu.Unlock()
	// The lazy catalog uses path identities for single-head files. Multi-head
	// workspaces use the materialized adapter and retain per-head exclusions.
	for _, path := range paths {
		keys = append(keys, desktopSourceKey(path, ""))
	}
	if len(keys) == 0 {
		return existing
	}
	sort.Strings(keys)
	body, _ := json.Marshal(keys)
	return string(body)
}
