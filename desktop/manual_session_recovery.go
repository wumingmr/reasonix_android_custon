package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"reasonix/internal/session"
)

// Selecting a persisted creation's own tab must not build another controller
// or wait for the background builder. Composer storage is available already;
// submission stays gated by readiness and completion never changes selection.
func (a *App) openManualCreationSurface(ref session.SessionRef, navigationSequence uint64) (bool, error) {
	defer a.lockRuntimeMutation("open manual creation surface")()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.desktopSessions.navigationSeq.Load() != navigationSequence {
		return false, errSessionNavigationSuperseded
	}
	for _, tab := range a.tabs {
		if !tab.removed && tab.SessionID == ref.SessionID && tab.PendingCreateOperationID != "" {
			a.activeTabID = tab.ID
			a.saveTabsLocked()
			return true, nil
		}
	}
	return false, nil
}

func (a *App) startManualSessionTab(ctx context.Context, tab *WorkspaceTab, report func(string)) error {
	// Build registration and shutdown admission share the same boundary.
	a.manualCreationMu.Lock()
	if a.shuttingDown.Load() || ctx.Err() != nil {
		a.manualCreationMu.Unlock()
		return context.Canceled
	}
	a.mu.RLock()
	ready, build := tab.Ctrl != nil, tab.buildExecution
	a.mu.RUnlock()
	if !ready && build == nil {
		a.startTabControllerBuildMode(tab, true)
	}
	a.manualCreationMu.Unlock()
	for {
		a.mu.RLock()
		builds := make([]*tabBuildExecution, 0, len(tab.buildExecutions))
		for execution := range tab.buildExecutions {
			builds = append(builds, execution)
		}
		a.mu.RUnlock()
		if len(builds) == 0 {
			break
		}
		for _, execution := range builds {
			select {
			case <-execution.done:
			case <-ctx.Done():
				report("stopping")
				for _, pending := range builds {
					pending.cancel()
				}
				<-execution.done // Only actual execution exit closes this channel.
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if tab.Ctrl == nil || tab.removed {
		return errors.New("session runtime did not start: " + tab.StartupErr)
	}
	return nil
}

func (a *App) ListManualSessionCreations() (views []ManualSessionCreationView, err error) {
	defer func() { err = sessionUIError(err, "", "") }()
	rows, err := a.sessionUIStore().List(a.bootContext(), "creation")
	views = []ManualSessionCreationView{}
	if err != nil {
		return views, err
	}
	for _, row := range rows {
		var view ManualSessionCreationView
		if err := json.Unmarshal(row.Payload, &view); err != nil {
			return views, err
		}
		if view.Phase != "ready" {
			views = append(views, a.creationView(view))
		}
	}
	return views, nil
}

func (a *App) reconcileManualSessionCreations() {
	m := a.creationManager()
	select {
	case <-a.tabsRestoredSignal():
		m.armRecovery()
	case <-m.ctx.Done():
	}
}

func (a *App) stopManualCreations() error {
	a.manualCreationMu.Lock()
	m := a.manualCreations
	a.manualCreationMu.Unlock()
	if m == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return m.CancelAndWait(ctx)
}
