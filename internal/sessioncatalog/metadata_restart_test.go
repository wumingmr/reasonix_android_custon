package sessioncatalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"reasonix/internal/historywork"
)

func TestMetadataQueueSupersedesInvalidScanBeforeReadingItsRemainder(t *testing.T) {
	dir := t.TempDir()
	for i := range 3 * historywork.BatchEntries {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d.jsonl", i)), []byte("unread body\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce, firstSlice sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); _ = c.Close(context.Background()) })
	starts, firstRows := 0, 0
	c.testReconcileStartHook = func(DirectoryTarget) { starts++ }
	c.testReconcileBatchHook = func(count int) {
		if starts == 1 {
			firstRows += count
			firstSlice.Do(func() {
				close(entered)
				<-release
			})
		}
	}
	target := DirectoryTarget{Path: dir, Scope: "global"}
	done, accepted := c.ScheduleReconcile(target)
	if !accepted {
		t.Fatal("initial root rejected")
	}
	c.ResumeDiscovery()
	<-entered
	// The scan is held before publishing its first slice, so the invalidation
	// deterministically arrives before it can read a second directory batch.
	joined, accepted := c.ScheduleReconcile(target)
	if !accepted || joined != done {
		t.Fatal("replacement lost shared completion")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("replacement did not settle")
	}
	if count, err := c.CountDirectorySessions(t.Context(), dir); err != nil || count != 3*historywork.BatchEntries {
		t.Fatalf("replacement lost a source: count=%d err=%v", count, err)
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if starts != 2 || firstRows > historywork.BatchEntries {
		t.Fatalf("obsolete iterator consumed the remainder: starts=%d firstRows=%d", starts, firstRows)
	}
}

func TestMetadataRestartDebounceCannotExtendItsAdmissionDeadline(t *testing.T) {
	now := time.Unix(100, 0)
	job := &metadataQueueJob{}
	deferMetadataRestart(job, now)
	deadline := job.restartDeadline
	if !job.ready.Equal(now.Add(historywork.PauseDuration)) {
		t.Fatal("restart did not yield a bounded slice")
	}
	for i := 1; i <= 20; i++ {
		deferMetadataRestart(job, now.Add(time.Duration(i)*50*time.Millisecond))
		if job.restartDeadline != deadline || job.ready.After(deadline) {
			t.Fatal("continued notifications extended the admission deadline")
		}
	}
	if job.ready.After(now.Add(time.Second)) {
		t.Fatal("continuous updates starved discovery admission")
	}
}
