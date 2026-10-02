package main

import (
	"sync/atomic"
	"testing"

	"reasonix/internal/event"
)

type countedRuntimeReader struct {
	bindingRuntimeReader
	reads atomic.Int32
}

func (r *countedRuntimeReader) RuntimeStateSnapshot() event.RuntimeStateSnapshot {
	r.reads.Add(1)
	return r.bindingRuntimeReader.RuntimeStateSnapshot()
}

func TestRuntimeStateUpdateDoesNotResampleUnchangedBindings(t *testing.T) {
	isolateDesktopUserDirs(t)
	first, second := &countedRuntimeReader{}, &countedRuntimeReader{}
	first.state = event.RuntimeStateSnapshot{SchemaVersion: 1, RuntimeEpoch: "first", Revision: 1}
	second.state = event.RuntimeStateSnapshot{SchemaVersion: 1, RuntimeEpoch: "second", Revision: 1}
	a := &WorkspaceTab{ID: "a", Ctrl: first}
	b := &WorkspaceTab{ID: "b", Ctrl: second}
	app := &App{tabs: map[string]*WorkspaceTab{"a": a, "b": b}}
	app.GetRuntimeStateSnapshot()
	for revision := uint64(2); revision <= 20; revision++ {
		state := first.state
		state.Revision = revision
		app.runtimeStateSnapshotWithUpdate(&localRuntimeUpdate{tab: a, ctrl: first, state: state})
	}
	if first.reads.Load() != 1 || second.reads.Load() != 1 {
		t.Fatal("incremental update sampled unrelated controllers")
	}
	app.GetRuntimeStateSnapshot()
	if second.reads.Load() != 2 {
		t.Fatal("explicit reconciliation did not sample the owner")
	}
	// An older callback must not regress an already-published producer revision.
	result := app.runtimeStateSnapshotWithUpdate(&localRuntimeUpdate{tab: a, ctrl: first, state: first.state})
	if result.Sessions[0].State.Revision != 20 {
		t.Fatal("late callback regressed the projection")
	}
	replacement := &countedRuntimeReader{}
	replacement.state = event.RuntimeStateSnapshot{RuntimeEpoch: "replacement", Revision: 1}
	app.mu.Lock()
	b.Ctrl = replacement
	app.mu.Unlock()
	app.runtimeStateSnapshotWithUpdate(&localRuntimeUpdate{tab: a, ctrl: first, state: first.state})
	if replacement.reads.Load() != 1 {
		t.Fatal("replacement inherited the old binding cache")
	}
}

func TestRuntimeStateQueuedUpdatesKeepNewestAndFenceRebinding(t *testing.T) {
	isolateDesktopUserDirs(t)
	reader := &countedRuntimeReader{}
	reader.state = event.RuntimeStateSnapshot{RuntimeEpoch: "owner", Revision: 1}
	tab := &WorkspaceTab{ID: "a", Ctrl: reader}
	app := &App{tabs: map[string]*WorkspaceTab{"a": tab}}
	app.GetRuntimeStateSnapshot()
	// Prevent the worker from sampling while all producers enqueue their cut.
	app.mu.Lock()
	for revision := uint64(100); revision > 1; revision-- {
		state := reader.state
		state.Revision = revision
		app.queueRuntimeProjection(localRuntimeUpdate{tab: tab, ctrl: reader, state: state})
	}
	app.mu.Unlock()
	app.flushRuntimeProjections()
	projection := app.runtimeStateSnapshotWithUpdate(&localRuntimeUpdate{tab: tab, ctrl: reader, state: reader.state})
	if projection.Sessions[0].State.Revision != 100 || reader.reads.Load() != 1 {
		t.Fatal("batched callbacks lost the newest revision or resampled the owner")
	}
	// A queued callback may outlive a same-controller session rebind.
	app.mu.Lock()
	tab.SessionGeneration = 2
	tab.SessionPath, tab.SessionID = "session:new", "new"
	app.mu.Unlock()
	projection = app.runtimeStateSnapshotWithUpdate(&localRuntimeUpdate{tab: tab, ctrl: reader,
		state: event.RuntimeStateSnapshot{RuntimeEpoch: "obsolete", Revision: 999}})
	if projection.Sessions[0].State.RuntimeEpoch == "obsolete" || reader.reads.Load() != 2 {
		t.Fatal("obsolete callback crossed the binding generation fence")
	}
	app.shuttingDown.Store(true)
	app.queueRuntimeProjection(localRuntimeUpdate{tab: tab, ctrl: reader})
	if app.runtimeStateProjection.events.done != nil {
		t.Fatal("shutdown admitted another projection worker")
	}
}
