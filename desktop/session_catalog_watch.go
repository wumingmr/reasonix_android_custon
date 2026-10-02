package main

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sort"
	"time"

	"github.com/fsnotify/fsnotify"
	"reasonix/internal/history"
	"reasonix/internal/sessioncatalog"
	"reasonix/internal/store"
)

// Directory notifications admit only dirty roots. The periodic audit remains
// authoritative after dropped notifications and on unsupported filesystems.
func (a *App) watchSessionCatalog(ctx context.Context, catalog *sessioncatalog.Catalog, metadataRequests <-chan struct{}, onAdmitted func()) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Reuse the platform directory backend. On macOS the generic kqueue
	// adapter enumerates children and opens a descriptor for every file.
	watcher, _ := newWorkspaceWatcher()
	var events <-chan fsnotify.Event
	var failures <-chan error
	if watcher != nil {
		defer watcher.Close()
		events, failures = watcher.Events(), watcher.Errors()
	}
	targets := map[string]sessioncatalog.DirectoryTarget{}
	watched := map[string]bool{}
	dirty := map[string]bool{}
	var batch *time.Timer
	var batchReady <-chan time.Time
	discoveryPending := false
	armBatch := func() {
		if (len(dirty) > 0 || discoveryPending) && batchReady == nil {
			batch = time.NewTimer(250 * time.Millisecond)
			batchReady = batch.C
		}
	}
	defer func() {
		if batch != nil {
			batch.Stop()
		}
	}()
	// A slow registry projection must not stop receiving filesystem events.
	// One worker coalesces refreshes and is joined before this owner returns.
	refreshMetadata, metadataDone := startCatalogMetadataRefresh(ctx, func(ctx context.Context) {
		if err := a.syncSessionCatalogMetadataBounded(ctx, catalog); err != nil && !errors.Is(err, context.Canceled) {
			slog.Debug("desktop: refresh session catalog metadata", "err", err)
		}
	})
	defer func() { cancel(); <-metadataDone }()
	refreshTargets := func() {
		targets = refreshCatalogWatchTargets(watcher, targets, a.sessionCatalogTargets(), watched, dirty)
	}
	refreshTargets()
	restored := a.tabsRestoredSignal()
	admitted := false
	metadata := time.NewTicker(30 * time.Second)
	audit := time.NewTicker(5 * time.Minute)
	defer metadata.Stop()
	defer audit.Stop()
	maintenance := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-catalog.Invalidated():
			return
		case <-metadataRequests:
			if admitted {
				refreshMetadata()
			}
		case <-restored:
			restored = nil
			// Restored identities can reveal additional roots. Register those
			// watches before allowing their queued discovery to run.
			refreshTargets()
			history.RegisterCatalogRoots(historyCatalogRoots(a.sessionCatalogTargets()))
			a.indexRestoredSessionPaths(ctx, catalog)
			// Query/UI admission must not wait for journal writes. The first
			// batch admits watched roots before releasing restored scan jobs.
			discoveryPending = true
			admitted = true
			onAdmitted()
			// First discovery covers the notification backlog before this barrier.
			// Per-file admission would saturate the queue and schedule a second
			// whole-root pass. Queries are already admitted.
			catchUpCatalogWatch(watcher, events, failures, targets, watched, dirty)
			refreshMetadata()
			a.requestHistoricalCatalogWithContext(ctx)
			armBatch()
		case event, ok := <-events:
			if !ok {
				events = nil
				clear(watched)
				continue
			}
			admitCatalogWatchEvent(catalog, event, targets, watched, dirty, watcher, !admitted || discoveryPending)
			armBatch()
		case _, ok := <-failures:
			if !ok {
				failures = nil
			}
			for key := range targets {
				dirty[key] = true
			}
			armBatch()
		case <-metadata.C:
			if admitted {
				refreshMetadata()
			}
			refreshTargets()
			armBatch()
		case <-audit.C:
			refreshTargets()
			keys := make([]string, 0, len(targets))
			for key := range targets {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if len(keys) > 0 {
				dirty[keys[maintenance%len(keys)]] = true
				maintenance++
			}
			armBatch()
		case <-batchReady:
			batchReady = nil
			admitCatalogWatchBatch(ctx, catalog, targets, dirty, discoveryPending)
			discoveryPending = false
			// Failed journal writes must retain the invalidation for retry.
			armBatch()
		}
	}
}

func catchUpCatalogWatch(watcher workspaceWatcher, events <-chan fsnotify.Event, failures <-chan error, targets map[string]sessioncatalog.DirectoryTarget, watched, dirty map[string]bool) {
	if barrier, ok := watcher.(interface{ CatchUp() }); ok {
		barrier.CatchUp()
	}
	// Drain only the notifications already queued at the barrier. New events
	// still go through the ordinary loop; a continuously written directory
	// cannot hold discovery hostage by keeping this drain alive indefinitely.
	for count := len(events); count > 0; count-- {
		if event, ok := <-events; ok {
			admitCatalogWatchEvent(nil, event, targets, watched, dirty, watcher, true)
		}
	}
	for count := len(failures); count > 0; count-- {
		if _, ok := <-failures; ok {
			for key := range targets {
				dirty[key] = true
			}
		}
	}
}

type catalogWatchPathQueue interface {
	TryRequestIndexSession(sessioncatalog.DirectoryTarget, string) bool
}

func admitCatalogWatchEvent(catalog catalogWatchPathQueue, event fsnotify.Event, targets map[string]sessioncatalog.DirectoryTarget, watched, dirty map[string]bool, watcher workspaceWatcher, initialDiscovery bool) {
	key := filepath.Clean(filepath.Dir(event.Name))
	// A transcript/sidecar write invalidates one session, not its root.
	if target, exists := targets[key]; exists {
		if path := catalogSessionPathForEvent(event.Name); path != "" && event.Op&(fsnotify.Remove|fsnotify.Rename) == 0 {
			if initialDiscovery {
				dirty[key] = true
				return
			}
			// Platform events use the canonical watch path; retain the registered
			// access spelling when publishing the exact session identity.
			if !catalog.TryRequestIndexSession(target, filepath.Join(target.Path, filepath.Base(path))) {
				// The event loop owns overflow, just like an unscoped directory
				// event. Journal one coalesced root in the next batch and retain
				// it if admission fails, rather than blocking on every notice.
				dirty[key] = true
			}
			return
		}
	}
	if _, exists := targets[filepath.Clean(event.Name)]; exists {
		key = filepath.Clean(event.Name)
		if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
			// Native backends retain subscriptions until explicitly removed.
			// Drop the backend entry as well as our admission flag so Add can
			// watch a newly created directory at this same path.
			if watcher != nil {
				_ = watcher.Remove(key)
			}
			delete(watched, key)
		}
	}
	if _, exists := targets[key]; exists {
		dirty[key] = true
	}
}

func catalogSessionPathForEvent(path string) string {
	if store.IsSessionTranscriptName(filepath.Base(path)) {
		return path
	}
	if filepath.Ext(path) == ".meta" {
		candidate := path[:len(path)-len(".meta")]
		if store.IsSessionTranscriptName(filepath.Base(candidate)) {
			return candidate
		}
	}
	return ""
}

func refreshCatalogWatchTargets(watcher workspaceWatcher, current map[string]sessioncatalog.DirectoryTarget, targets []sessioncatalog.DirectoryTarget, watched, dirty map[string]bool) map[string]sessioncatalog.DirectoryTarget {
	next := map[string]sessioncatalog.DirectoryTarget{}
	for _, target := range targets {
		key := canonicalWorkspaceRoot(target.Path)
		next[key] = target
		_, known := current[key]
		if !known {
			dirty[key] = true
		}
		if watcher != nil && !watched[key] {
			watched[key] = watcher.Add(key, false) == nil
			if watched[key] && known {
				// Recovered watching must reconcile the unobserved interval.
				dirty[key] = true
			}
		}
		// Unavailable watches use the rotating audit, not a full-root scan
		// on every metadata refresh. Registration itself may still retry.
	}
	for key := range current {
		if _, exists := next[key]; !exists {
			if watcher != nil && watched[key] {
				_ = watcher.Remove(key)
			}
			delete(watched, key)
			delete(dirty, key)
		}
	}
	return next
}

func admitCatalogWatchBatch(ctx context.Context, catalog *sessioncatalog.Catalog, targets map[string]sessioncatalog.DirectoryTarget, dirty map[string]bool, discoveryPending bool) {
	initial := []sessioncatalog.DirectoryTarget{}
	for key := range dirty {
		delete(dirty, key)
		target, exists := targets[key]
		if !exists || ctx.Err() != nil {
			continue
		}
		if discoveryPending {
			initial = append(initial, target)
		} else {
			if !catalog.RequestReconcile(target) {
				dirty[key] = true
			}
		}
	}
	if discoveryPending {
		for _, target := range catalog.ResumeDiscovery(initial...) {
			dirty[canonicalWorkspaceRoot(target.Path)] = true
		}
	}
}
