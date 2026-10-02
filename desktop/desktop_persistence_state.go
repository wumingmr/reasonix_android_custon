package main

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"reasonix/desktop/internal/draftstate"
	"reasonix/desktop/internal/legacycleanup"
	"reasonix/desktop/internal/sessionui"
	"reasonix/internal/config"
)

// desktopPersistenceState groups process-lifetime stores and their recovery
// coordinator so App does not expose each lifecycle field independently.
type desktopPersistenceState struct {
	desktopSessions             desktopSessionState
	desktopDrafts               *draftstate.Store
	sessionUI                   *sessionui.Store
	sessionPresets              *sessionPresetStore
	manualCreationMu            sync.Mutex
	manualCreationTasks         sync.WaitGroup
	manualCreations             *manualCreationManager
	legacyCleanup               *legacycleanup.Store
	desktopMigrationDone        chan struct{}
	desktopMigrationFailed      atomic.Bool
	beforeSavedTabMigrationWait func()
	legacyCleanupWorker         legacyCleanupWorkerState
	historicalImports           historicalImportCoordinator
}

func newDesktopPersistenceState() desktopPersistenceState {
	return desktopPersistenceState{
		desktopSessions:      newDesktopSessionState(),
		desktopDrafts:        draftstate.New(config.DesktopDraftStatePath()),
		sessionUI:            sessionui.New(config.DesktopSessionUIStatePath()),
		sessionPresets:       newSessionPresetStore(),
		legacyCleanup:        legacycleanup.New(config.DesktopLegacyEmptySessionCleanupPath()),
		desktopMigrationDone: make(chan struct{}),
	}
}

func (a *App) startDesktopPersistenceReconciliation() {
	a.goSafe("reconcileCompletedLegacyCleanup", func() {
		state, err := a.legacyCleanup.Load(a.bootContext())
		if err != nil {
			slog.Warn("desktop: legacy cleanup recovery unavailable", "err", err)
			return
		}
		for _, item := range state.Items {
			if item.Restored {
				continue
			}
			// Only acknowledge actions already committed by the old binary.
			// Pending candidates must never initiate a new archive.
			if item.SessionID != "" {
				a.reconcileLegacyCleanupArchivedOperation(item, item.SessionID)
			}
			if item.Phase == "archive_pending" {
				a.reconcileLegacyCleanupTopicArchive(item)
			}
		}
	})
	a.goSafe("reconcileManualSessionCreations", a.reconcileManualSessionCreations)
	// Workspace drafts are retired, including unsent content and interrupted
	// draft submissions. Do not open or reconcile their historical database.
}
