package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/repair"
	"reasonix/internal/stats"
)

const (
	shutdownReasonUserQuit       = "user_quit"
	shutdownReasonUpdateRestart  = "update_restart"
	shutdownReasonStartupFailure = "startup_failure"
	shutdownReasonConnectionLost = "connection_lost"
	shutdownReasonSystemSignal   = "system_signal"
)

type shutdownRequest struct {
	RequestID string `json:"requestId"`
	Reason    string `json:"reason"`
}

type shutdownStatus struct {
	RequestID string `json:"requestId"`
	Reason    string `json:"reason"`
	Phase     string `json:"phase"`
	Outcome   string `json:"outcome"`
	Completed bool   `json:"completed"`
	Retryable bool   `json:"retryable"`
	ErrorCode string `json:"errorCode,omitempty"`
	Error     string `json:"error,omitempty"`
	UpdatedAt string `json:"updatedAt"`
}

type desktopShutdownItem struct {
	tab      *WorkspaceTab
	ctrl     control.SessionAPI
	readOnly bool
}

// desktopShutdownCoordinator owns the one process shutdown transaction. It
// retains successful save and close steps so a retry resumes at the failed
// step instead of closing an already released session a second time.
type desktopShutdownCoordinator struct {
	mu       sync.Mutex
	running  bool
	done     chan struct{}
	status   shutdownStatus
	frozen   bool
	items    []desktopShutdownItem
	saved    map[string]bool
	finished map[string]bool
}

type shutdownStepError struct {
	code string
	err  error
}

func (e *shutdownStepError) Error() string { return e.err.Error() }
func (e *shutdownStepError) Unwrap() error { return e.err }

func normalizeShutdownReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case shutdownReasonUserQuit, shutdownReasonUpdateRestart, shutdownReasonStartupFailure,
		shutdownReasonConnectionLost, shutdownReasonSystemSignal:
		return strings.TrimSpace(reason)
	default:
		return shutdownReasonConnectionLost
	}
}

func (a *App) shutdownState() *desktopShutdownCoordinator {
	a.shutdownMu.Lock()
	defer a.shutdownMu.Unlock()
	if a.shutdownCoordinator == nil {
		a.shutdownCoordinator = &desktopShutdownCoordinator{
			saved:    map[string]bool{},
			finished: map[string]bool{},
			status:   shutdownStatus{Phase: "idle", Outcome: "not_started"},
		}
	}
	return a.shutdownCoordinator
}

func (a *App) shutdownStatus(requestID string) shutdownStatus {
	c := a.shutdownState()
	c.mu.Lock()
	defer c.mu.Unlock()
	status := c.status
	if requestID != "" && status.RequestID != "" && requestID != status.RequestID {
		return shutdownStatus{
			RequestID: requestID, Phase: "idle", Outcome: "not_started", Retryable: true,
			ErrorCode: "shutdown_request_not_found",
		}
	}
	return status
}

func (a *App) requestShutdown(ctx context.Context, request shutdownRequest) (shutdownStatus, error) {
	request.RequestID = strings.TrimSpace(request.RequestID)
	if request.RequestID == "" {
		return shutdownStatus{}, &shutdownStepError{code: "invalid_request", err: errors.New("shutdown requestId is required")}
	}
	request.Reason = normalizeShutdownReason(request.Reason)
	c := a.shutdownState()

	c.mu.Lock()
	if c.status.Completed {
		status := c.status
		c.mu.Unlock()
		return status, nil
	}
	if c.running {
		done := c.done
		c.mu.Unlock()
		select {
		case <-done:
			status := a.shutdownStatus("")
			if status.Outcome == "failed" {
				return status, errors.New(status.Error)
			}
			return status, nil
		case <-ctx.Done():
			return a.shutdownStatus(""), ctx.Err()
		}
	}
	// A retry resumes the original transaction. Later EOF, signal, or RPC
	// requests must not rewrite the trigger that started it.
	if c.status.RequestID != "" {
		request.RequestID = c.status.RequestID
		request.Reason = c.status.Reason
	}
	c.running = true
	c.done = make(chan struct{})
	c.status = shutdownStatus{
		RequestID: request.RequestID,
		Reason:    request.Reason,
		Phase:     "preparing", Outcome: "in_progress",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	done := c.done
	c.mu.Unlock()

	err := a.runShutdown(c)
	if err != nil {
		status := a.shutdownStatus("")
		a.lifecycle.tracker.markShutdown(status.Reason, status.Phase, "failed")
	}
	c.mu.Lock()
	if err != nil {
		var step *shutdownStepError
		code := "shutdown_failed"
		if errors.As(err, &step) {
			code = step.code
		}
		c.status.Outcome = "failed"
		c.status.Retryable = true
		c.status.ErrorCode = code
		c.status.Error = err.Error()
		c.status.Completed = false
	} else {
		c.status.Phase = "completed"
		c.status.Outcome = "success"
		c.status.Completed = true
		c.status.Retryable = false
		c.status.ErrorCode = ""
		c.status.Error = ""
	}
	c.status.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	c.running = false
	close(done)
	status := c.status
	c.mu.Unlock()
	return status, err
}

func (c *desktopShutdownCoordinator) setPhase(phase string) {
	c.mu.Lock()
	c.status.Phase = phase
	c.status.Outcome = "in_progress"
	c.status.ErrorCode = ""
	c.status.Error = ""
	c.status.Retryable = false
	c.status.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	c.mu.Unlock()
}

func (a *App) runShutdown(c *desktopShutdownCoordinator) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &shutdownStepError{code: "cleanup_panic", err: fmt.Errorf("shutdown cleanup panic: %v", recovered)}
		}
	}()

	c.mu.Lock()
	frozen := c.frozen
	reason := c.status.Reason
	c.mu.Unlock()
	if !frozen {
		c.setPhase("cancelling_background")
		a.lifecycle.tracker.markShutdown(reason, "cancelling_background", "in_progress")
		a.manualCreationMu.Lock()
		a.shuttingDown.Store(true)
		m := a.manualCreations
		a.manualCreationMu.Unlock()
		if m != nil {
			m.StopAdmission()
		}
		c.runStep("cancel-session-navigation", a.cancelSessionNavigation)
		c.runStep("cancel-tab-builds", a.cancelAllTabBuilds)
		if err := c.runErrorStep("manual-session-creation", a.stopManualCreations); err != nil {
			return &shutdownStepError{code: "manual_creation_stop_timeout", err: err}
		}
		c.runStep("cancel-session-exports", a.cancelSessionExports)
		c.runStep("runtime-projections", a.flushRuntimeProjections)
		c.runStep("tab-layout", a.flushTabLayoutWrites)
		c.runStep("historical-imports", a.stopHistoricalImports)
		c.runStep("session-catalog", func() { a.stopSessionCatalog(250 * time.Millisecond) })
		c.mu.Lock()
		c.frozen = true
		c.mu.Unlock()
	}

	// Use the normal runtime lock order and never hold App.mu while invoking a
	// controller. This prevents callback re-entry deadlocks during snapshots.
	c.setPhase("waiting_runtime_rebuild")
	a.lifecycle.tracker.markShutdown(reason, "waiting_runtime_rebuild", "in_progress")
	a.runtimeRebuildMu.Lock()
	defer a.runtimeRebuildMu.Unlock()
	c.setPhase("waiting_runtime_admission")
	a.lifecycle.tracker.markShutdown(reason, "waiting_runtime_admission", "in_progress")
	a.runtimeAdmissionMu.Lock()
	defer a.runtimeAdmissionMu.Unlock()

	c.mu.Lock()
	needsItems := c.items == nil
	c.mu.Unlock()
	if needsItems {
		a.mu.RLock()
		tabs := a.runtimeTabsLocked()
		items := make([]desktopShutdownItem, 0, len(tabs))
		for _, tab := range tabs {
			if tab.Ctrl != nil {
				items = append(items, desktopShutdownItem{tab: tab, ctrl: tab.Ctrl, readOnly: tab.ReadOnly})
			}
		}
		a.mu.RUnlock()
		sort.Slice(items, func(i, j int) bool { return items[i].tab.ID < items[j].tab.ID })
		c.mu.Lock()
		c.items = items
		c.mu.Unlock()
	}
	c.mu.Lock()
	items := append([]desktopShutdownItem(nil), c.items...)
	reason = c.status.Reason
	c.mu.Unlock()

	c.setPhase("saving")
	a.lifecycle.tracker.markShutdown(reason, "saving", "in_progress")
	for _, item := range items {
		if item.readOnly || historicalPreview(item.ctrl) {
			continue
		}
		c.mu.Lock()
		saved := c.saved[item.tab.ID]
		c.mu.Unlock()
		if saved {
			continue
		}
		if err := item.ctrl.SnapshotForShutdown(); err != nil {
			a.lifecycle.tracker.markShutdown(reason, "saving", "failed")
			return &shutdownStepError{code: "session_save_failed", err: fmt.Errorf("save session %s: %w", item.tab.ID, err)}
		}
		c.mu.Lock()
		c.saved[item.tab.ID] = true
		c.mu.Unlock()
	}

	c.setPhase("closing")
	a.lifecycle.tracker.markShutdown(reason, "closing", "in_progress")
	if err := a.shutdownBody(c, items); err != nil {
		return err
	}
	a.lifecycle.tracker.markShutdown(reason, "completed", "success")
	if reason == shutdownReasonUserQuit || reason == shutdownReasonUpdateRestart {
		a.lifecycle.tracker.clean()
	}
	return nil
}

// completeDesktopShutdown remains the small panic-preserving primitive used by
// lifecycle compatibility tests and callers outside the coordinated App path.
func completeDesktopShutdown(tracker *desktopLifecycleTracker, body func()) {
	tracker.stopWriter()
	tracker.mark("shutting_down")
	body()
	tracker.clean()
}

func (a *App) shutdownBody(c *desktopShutdownCoordinator, items []desktopShutdownItem) error {
	if a.sessionUI != nil {
		if err := c.runErrorStep("session-ui", a.sessionUI.Close); err != nil {
			return &shutdownStepError{code: "session_ui_close_failed", err: err}
		}
	}
	if a.desktopDrafts != nil {
		if err := c.runErrorStep("desktop-drafts", a.desktopDrafts.Close); err != nil {
			return &shutdownStepError{code: "draft_close_failed", err: err}
		}
	}
	c.runStep("workspace-preview", a.stopWorkspacePreviewOrigin)
	if a.desktopShell.coordinator != nil {
		c.runStep("shell-coordinator", a.desktopShell.coordinator.stop)
	}
	if a.workspaceHub != nil {
		c.runStep("workspace-hub", a.workspaceHub.close)
	}
	c.runStep("remote-windows", a.closeAllRemoteWindows)
	c.runStep("deferred-rebuild", a.stopDeferredRebuildRetry)
	c.runStep("takeover-mirrors-stop", a.stopTakeoverMirrors)
	if a.heartbeat != nil {
		c.runStep("heartbeat", a.heartbeat.Stop)
	}
	c.runStep("bot-runtime", a.stopBotRuntime)
	c.runStep("bot-bridge", a.stopBotBridge)
	c.runStep("remote-runtime", a.stopRemoteRuntime)
	c.runStep("tray", a.stopTray)
	if a.terminals != nil {
		c.runStep("terminals", a.terminals.closeAll)
	}
	c.runStep("window-state", a.saveWindowStateSync)

	for _, item := range items {
		c.runStep("session:"+item.tab.ID, func() {
			item.ctrl.Close()
			if !a.returnTakeoverLeaseForShutdown(item.tab) {
				item.tab.releaseSessionLease()
			}
			a.mu.Lock()
			a.releaseSessionRuntimeLocked(item.tab)
			a.mu.Unlock()
		})
	}
	c.runStep("takeover-mirrors-end", a.endTakeoverMirrors)
	c.runStep("update-health", func() {
		if !a.startupReady.Load() {
			return
		}
		if err := a.commitPendingUpdateHealth(); err != nil {
			slog.Warn("desktop: commit healthy update during shutdown", "err", err)
		}
		if archived, err := archiveSupersededPendingUpdateAfterReady(); err != nil {
			slog.Warn("desktop: retire superseded update during shutdown", "err", err)
		} else if archived {
			slog.Info("desktop: archived superseded update transaction during shutdown")
		}
		_ = repair.RecordHealthyConfig(version)
	})
	c.runStep("shared-hosts", a.closeAllSharedHosts)
	c.runStep("derived-state", func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		if err := stats.Flush(flushCtx, config.StatsDir()); err != nil {
			slog.Warn("desktop: flush shutdown stats", "err", err)
		}
		if err := flushDesktopDerivedCatalogs(flushCtx); err != nil {
			slog.Warn("desktop: flush derived catalogs", "err", err)
		}
	})
	if a.topicState != nil {
		c.runStep("topic-state", a.topicState.close)
	}
	if err := c.runErrorStep("session-services", a.closeSessionServicesResult); err != nil {
		return &shutdownStepError{code: "session_service_close_failed", err: err}
	}
	// Last: every controller that subscribed to the host watcher is gone, and
	// boot.Build leaves a caller-owned service alone, so this is the only close.
	c.runStep("skill-watch-service", a.closeSharedSkillWatchService)
	return nil
}
