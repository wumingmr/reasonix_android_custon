package sessioncatalog

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestMetadataResumeCoalescesWatchedRootsWithInterruptedJournal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "one.jsonl"), []byte("unread body"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true, StartPaused: true}
	c, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !c.RequestReconcile(DirectoryTarget{Path: root, Scope: "project", WorkspaceRoot: root}) {
		t.Fatal("initial journal entry rejected")
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err = Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	// Attach to the restored completion before dispatch. The watcher then
	// supplies a newer scope, as it does after restoring project identities.
	done, accepted := c.ScheduleReconcile(DirectoryTarget{Path: root, Scope: "project", WorkspaceRoot: root})
	if !accepted {
		t.Fatal("restored task rejected")
	}
	var starts []DirectoryTarget
	c.testReconcileStartHook = func(target DirectoryTarget) { starts = append(starts, target) }
	if rejected := c.ResumeDiscovery(DirectoryTarget{Path: root, Scope: "global"}); len(rejected) != 0 {
		t.Fatalf("watched root rejected: %+v", rejected)
	}
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("resumed scan did not settle")
	}
	var pending int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM catalog_pending_roots`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("completed journal=%d: %v", pending, err)
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(starts) != 1 || starts[0].Scope != "global" {
		t.Fatalf("startup must dispatch latest watched identity once: %+v", starts)
	}
}

// Both roots have queue jobs while the first holds a slice. The second's
// invalidation precedes dispatch and must not become a redundant follow-up.
func TestMetadataQueueCoalescesUpdatesWhileWaitingForDispatch(t *testing.T) {
	first, waiting := t.TempDir(), t.TempDir()
	for _, dir := range []string{first, waiting} {
		if err := os.WriteFile(filepath.Join(dir, "one.jsonl"), []byte("unread body"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var gate sync.Once
	t.Cleanup(func() { gate.Do(func() { close(release) }); _ = c.Close(context.Background()) })
	var slice sync.Once
	c.testReconcileBatchHook = func(int) {
		slice.Do(func() {
			close(entered)
			<-release
		})
	}
	var starts []DirectoryTarget
	c.testReconcileStartHook = func(target DirectoryTarget) { starts = append(starts, target) }
	c.PrioritizeWorkspace("global", "")
	if !c.RequestReconcile(DirectoryTarget{Path: first, Scope: "global"}) {
		t.Fatal("first root rejected")
	}
	done, accepted := c.ScheduleReconcile(DirectoryTarget{Path: waiting, Scope: "project", WorkspaceRoot: waiting})
	if !accepted {
		t.Fatal("waiting root rejected")
	}
	c.ResumeDiscovery()
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal("first scan never entered")
	}
	// Persisted invalidations retain their latest scope and sequence too.
	joined, accepted := c.ScheduleReconcile(DirectoryTarget{Path: waiting, Scope: "global"})
	if !accepted || joined != done {
		t.Fatal("waiting invalidation did not join the existing completion signal")
	}
	gate.Do(func() { close(release) })
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("waiting root did not settle")
	}
	var pending int
	if err := c.db.QueryRow(`SELECT count(*) FROM catalog_pending_roots WHERE path_key=?`, queuePathKey(waiting)).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("latest pre-dispatch journal entry was not retired: %d %v", pending, err)
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, target := range starts {
		if target.Path == waiting {
			count++
			if target.Scope != "global" {
				t.Fatalf("dispatched stale scope: %+v", target)
			}
		}
	}
	if count != 1 {
		t.Fatalf("pre-dispatch invalidation caused %d scans, want 1", count)
	}
}

func TestMetadataQueuePreservesInvalidationAfterDispatch(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var gate, slice sync.Once
	t.Cleanup(func() { gate.Do(func() { close(release) }); _ = c.Close(context.Background()) })
	c.testReconcileBatchHook = func(int) { slice.Do(func() { close(entered); <-release }) }
	starts := 0
	c.testReconcileStartHook = func(DirectoryTarget) { starts++ }
	target := DirectoryTarget{Path: dir, Scope: "global"}
	done, accepted := c.ScheduleReconcile(target)
	if !accepted {
		t.Fatal("initial discovery rejected")
	}
	c.ResumeDiscovery()
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal("scan never entered")
	}
	// The iterator has already reached EOF. A late file must be discovered by
	// a follow-up, not swallowed by pre-dispatch coalescing.
	path := filepath.Join(dir, "late.jsonl")
	if err := os.WriteFile(path, []byte("unread body"), 0600); err != nil {
		t.Fatal(err)
	}
	joined, accepted := c.ScheduleReconcile(target)
	if !accepted || joined != done {
		t.Fatal("late invalidation lost the shared completion signal")
	}
	gate.Do(func() { close(release) })
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("follow-up did not settle")
	}
	if _, found, err := c.GetSession(t.Context(), path); err != nil || !found {
		t.Fatalf("late source was lost: %v %v", found, err)
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if starts != 2 {
		t.Fatalf("in-flight invalidation caused %d scans, want 2", starts)
	}
}
