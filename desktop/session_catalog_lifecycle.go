package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"

	"reasonix/internal/history"
	"reasonix/internal/projectiondb"
	"reasonix/internal/sessioncatalog"
	"reasonix/internal/taskcatalog"
)

func (a *App) runSessionCatalog(ctx context.Context, initialReconcileDone chan struct{}, metadataRequests <-chan struct{}) {
	initialReconcileFinished := false
	defer func() {
		if !initialReconcileFinished {
			close(initialReconcileDone)
		}
	}()
	path := sessioncatalog.DefaultPath()
	freshGeneration := false
	if strings.TrimSpace(path) != "" {
		_, statErr := os.Stat(path)
		freshGeneration = errors.Is(statErr, os.ErrNotExist)
	}
	targets := a.sessionCatalogTargets()
	history.RegisterCatalogRoots(historyCatalogRoots(targets))
	projects := loadProjectsFile()
	taskcatalog.RegisterSharedProject(globalWorkspaceRoot(), projects.GlobalTitle)
	for _, project := range projects.Projects {
		taskcatalog.RegisterSharedProject(project.Root, projectDisplayName(project))
	}
	var revisionFloor uint64
	deferredIntegrity := true
	for ctx.Err() == nil {
		catalog, err := sessioncatalog.Open(ctx, sessioncatalog.Options{
			Path: path, MetadataOnly: true, StartPaused: true,
			DeferredMetadataIntegrity: deferredIntegrity, RevisionFloor: revisionFloor,
			Maintenance: &a.historyMaintenance,
			OnDiscovery: func(event sessioncatalog.DiscoveryEvent) {
				slog.Info("desktop: history discovery", "root", event.Root, "sequence", event.Sequence,
					"phase", event.Phase, "origin", event.Origin, "failure", event.Failure)
				if event.Phase == "completed" {
					a.requestHistoricalLegacyReconciliation(ctx)
				}
			},
			OnRevision: func(revision uint64, roots []string, reason string) {
				a.emitProjectTreeChangedV2(revision, roots, reason)
			},
		})
		if err != nil {
			if deferredIntegrity && projectiondb.IsCorruptionError(err) {
				deferredIntegrity = false
				continue
			}
			slog.Warn("desktop: open session catalog", "err", err)
			return
		}
		// Pair publication with stopSessionCatalog's lifecycle lock. A stopped
		// owner cannot publish a replacement after shutdown removed its pointer.
		a.catalogLifecycleMu.Lock()
		stopped := ctx.Err() != nil || a.shuttingDown.Load()
		if !stopped {
			a.sessionCatalog.Store(catalog)
		}
		a.catalogLifecycleMu.Unlock()
		if stopped {
			_ = catalog.Close(context.Background())
			return
		}
		if freshGeneration {
			catalog.MarkRepairReason("generation_upgrade")
		}
		a.watchSessionCatalog(ctx, catalog, metadataRequests, func() {
			if !initialReconcileFinished {
				close(initialReconcileDone)
				initialReconcileFinished = true
			}
		})
		select {
		case <-catalog.Invalidated():
			// Invalidate the published owner before closing every read lease.
			// Only then may the normal validating open quarantine the database.
			a.sessionCatalog.CompareAndSwap(catalog, nil)
			if err := catalog.Close(context.Background()); err != nil {
				slog.Warn("desktop: close invalid catalog", "err", err)
				return
			}
			revisionFloor = catalog.Status().Revision + 1
			deferredIntegrity, freshGeneration = false, true
		default:
			return
		}
	}
}
