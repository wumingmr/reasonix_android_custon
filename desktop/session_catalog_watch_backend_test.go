package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"reasonix/internal/sessioncatalog"
)

type recoveringCatalogWatch struct {
	workspaceWatcher
	fail    bool
	adds    int
	removes []string
}

type catchUpCatalogWatcher struct {
	workspaceWatcher
	catchUp func()
}

func (w *catchUpCatalogWatcher) CatchUp() { w.catchUp() }

type saturatedCatalogPathQueue struct {
	attempts int
}

func (q *saturatedCatalogPathQueue) TryRequestIndexSession(sessioncatalog.DirectoryTarget, string) bool {
	q.attempts++
	return false
}

func TestCatalogWatchPathOverflowCoalescesAtEventOwner(t *testing.T) {
	root := canonicalWorkspaceRoot(t.TempDir())
	target := sessioncatalog.DirectoryTarget{Path: root, Scope: "global"}
	targets := map[string]sessioncatalog.DirectoryTarget{root: target}
	watching, dirty := map[string]bool{root: true}, map[string]bool{}
	queue := &saturatedCatalogPathQueue{}
	for i := range 4096 {
		admitCatalogWatchEvent(queue, fsnotify.Event{Name: filepath.Join(root, fmt.Sprintf("%d.jsonl", i)), Op: fsnotify.Write}, targets, watching, dirty, nil, false)
	}
	if queue.attempts != 4096 || len(dirty) != 1 || !dirty[root] {
		t.Fatalf("overflow lost its coalesced root: attempts=%d dirty=%v", queue.attempts, dirty)
	}
}

func TestCatalogWatchInitialBacklogCoalescesBeforeDiscovery(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	path := filepath.Join(dir, "old.jsonl")
	if err := os.WriteFile(path, []byte("unreadable body"), 0600); err != nil {
		t.Fatal(err)
	}
	var requested, started atomic.Int32
	catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{InMemory: true, MetadataOnly: true, StartPaused: true,
		OnDiscovery: func(event sessioncatalog.DiscoveryEvent) {
			if event.Phase == "requested" {
				requested.Add(1)
			}
			if event.Phase == "started" {
				started.Add(1)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	key, otherKey := canonicalWorkspaceRoot(dir), canonicalWorkspaceRoot(other)
	target := sessioncatalog.DirectoryTarget{Path: dir, Scope: "global"}
	targets := map[string]sessioncatalog.DirectoryTarget{key: target, otherKey: {Path: other, Scope: "global"}}
	watched, dirty := map[string]bool{key: true, otherKey: true}, map[string]bool{}
	// Force more notices than the exact-path queue can retain. Before the
	// first iterator, all of them belong to the same root invalidation.
	for range 4096 {
		admitCatalogWatchEvent(catalog, fsnotify.Event{Name: filepath.Join(key, "old.jsonl"), Op: fsnotify.Create}, targets, watched, dirty, nil, true)
	}
	events, failures := make(chan fsnotify.Event, 2), make(chan error, 1)
	barrierReached := false
	watcher := &catchUpCatalogWatcher{catchUp: func() {
		barrierReached = true
		events <- fsnotify.Event{Name: filepath.Join(key, "old.jsonl"), Op: fsnotify.Write}
		failures <- fsnotify.ErrEventOverflow
	}}
	catchUpCatalogWatch(watcher, events, failures, targets, watched, dirty)
	if !barrierReached || len(events) != 0 || len(failures) != 0 || !dirty[key] || !dirty[otherKey] {
		t.Fatalf("initial barrier lost notifications: reached=%v dirty=%v", barrierReached, dirty)
	}
	if requested.Load() != 0 {
		t.Fatalf("pre-discovery backlog admitted %d redundant reconciles", requested.Load())
	}
	done, accepted := catalog.ScheduleReconcile(target)
	if !accepted {
		t.Fatal("initial discovery rejected")
	}
	catalog.ResumeDiscovery()
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("initial discovery did not finish")
	}
	if started.Load() != 1 {
		t.Fatalf("initial backlog started %d scans", started.Load())
	}
	if _, found, err := catalog.GetSession(t.Context(), path); err != nil || !found {
		t.Fatalf("first scan omitted historical source: found=%v err=%v", found, err)
	}
	clear(dirty)
	admitCatalogWatchEvent(catalog, fsnotify.Event{Name: key, Op: fsnotify.Write}, targets, watched, dirty, nil, false)
	if !dirty[key] {
		t.Fatal("post-discovery directory invalidation was swallowed")
	}
}

func (w *recoveringCatalogWatch) Remove(path string) error {
	w.removes = append(w.removes, path)
	return nil
}

func (w *recoveringCatalogWatch) Add(string, bool) error {
	w.adds++
	if w.fail {
		return errors.New("watch temporarily unavailable")
	}
	return nil
}

func TestCatalogWatchRecoveryReconcilesOnlyTheUnobservedInterval(t *testing.T) {
	root := canonicalWorkspaceRoot(t.TempDir())
	targets := []sessioncatalog.DirectoryTarget{{Path: root, Scope: "global"}}
	watched, dirty := map[string]bool{}, map[string]bool{}
	watcher := &recoveringCatalogWatch{fail: true}
	current := refreshCatalogWatchTargets(watcher, nil, targets, watched, dirty)
	if !dirty[root] || watched[root] {
		t.Fatal("failed initial watch omitted initial discovery")
	}
	clear(dirty)
	current = refreshCatalogWatchTargets(watcher, current, targets, watched, dirty)
	if len(dirty) != 0 || watcher.adds != 2 {
		t.Fatal("watch retry must not schedule full discovery on every metadata tick")
	}
	watcher.fail = false
	current = refreshCatalogWatchTargets(watcher, current, targets, watched, dirty)
	if !dirty[root] || !watched[root] {
		t.Fatal("recovered watch failed to reconcile its observation gap")
	}
	clear(dirty)
	refreshCatalogWatchTargets(watcher, current, targets, watched, dirty)
	if len(dirty) != 0 || watcher.adds != 3 {
		t.Fatal("settled watch restarted registration or discovery")
	}
}

func TestCatalogWatchCanonicalEventKeepsRegisteredAccessPath(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(source, []byte("not a valid transcript"), 0600); err != nil {
		t.Fatal(err)
	}
	updated := make(chan struct{}, 1)
	catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{InMemory: true, MetadataOnly: true, StartPaused: true,
		OnRevision: func(uint64, []string, string) {
			select {
			case updated <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	watched, dirty := map[string]bool{}, map[string]bool{}
	targets := refreshCatalogWatchTargets(nil, nil, []sessioncatalog.DirectoryTarget{{Path: dir, Scope: "global"}}, watched, dirty)
	clear(dirty)
	key := canonicalWorkspaceRoot(dir)
	admitCatalogWatchEvent(catalog, fsnotify.Event{Name: filepath.Join(key, "session.jsonl.meta"), Op: fsnotify.Write}, targets, watched, dirty, nil, false)
	if len(dirty) != 0 {
		t.Fatalf("exact metadata event scheduled a root scan: %v", dirty)
	}
	select {
	case <-updated:
	case <-time.After(sessionCatalogTestDeadline):
		t.Fatal("canonical event failed to update the exact path")
	}
	record, exists, err := catalog.GetSession(t.Context(), source)
	if err != nil || !exists || record.Path != source {
		t.Fatalf("watch event changed access identity: %+v %v %v", record, exists, err)
	}
	// A removed root must drop its watch and schedule reconciliation even
	// when there is no current filesystem object to canonicalize.
	watched[key] = true
	watcher := &recoveringCatalogWatch{}
	admitCatalogWatchEvent(catalog, fsnotify.Event{Name: key, Op: fsnotify.Remove}, targets, watched, dirty, watcher, false)
	if watched[key] || !dirty[key] {
		t.Fatalf("root removal lost invalidation: watched=%v dirty=%v", watched, dirty)
	}
	if len(watcher.removes) != 1 || watcher.removes[0] != key {
		t.Fatalf("root removal retained the native subscription: %v", watcher.removes)
	}
	clear(dirty)
	refreshCatalogWatchTargets(watcher, targets, []sessioncatalog.DirectoryTarget{{Path: dir, Scope: "global"}}, watched, dirty)
	if watcher.adds != 1 || !watched[key] || !dirty[key] {
		t.Fatal("replacement directory did not re-register and reconcile")
	}
}
