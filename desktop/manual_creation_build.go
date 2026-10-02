package main

import (
	"context"
	"reasonix/internal/control"
)

// Register only after boot success. Publication locks unwind before this join;
// a Close request alone can leave controller-owned persistence running.
func finishUnpublishedTabController(ctrl control.SessionAPI, generation uint64, published *bool) {
	if generation == 0 || *published || ctrl == nil {
		return
	}
	ctrl.Close()
	if closed, ok := ctrl.(interface{ Closed() <-chan struct{} }); ok {
		<-closed.Closed()
	}
}

// Unlike buildDone, done cannot be closed by a replacement generation.
type tabBuildExecution struct {
	done       chan struct{}
	cancel     context.CancelFunc
	generation uint64
}

func (a *App) finishTabBuildExecution(tab *WorkspaceTab, execution *tabBuildExecution) {
	a.mu.Lock()
	if tab.buildExecution == execution {
		tab.buildExecution = nil
	}
	delete(tab.buildExecutions, execution)
	close(execution.done)
	a.mu.Unlock()
}

func (a *App) reportManualBuildStage(tab *WorkspaceTab, generation uint64, stage string) {
	a.mu.RLock()
	if tab == nil || tab.buildGeneration != generation {
		a.mu.RUnlock()
		return
	}
	id := tab.PendingCreateOperationID
	a.mu.RUnlock()
	a.manualCreationMu.Lock()
	m := a.manualCreations
	a.manualCreationMu.Unlock()
	if m == nil || id == "" {
		return
	}
	m.mu.Lock()
	t := m.tasks[id]
	if t != nil && !t.running {
		t = nil
	}
	m.mu.Unlock()
	if t != nil {
		m.setStageForGeneration(t, "running", stage, generation)
	}
}
