package sessioncatalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMetadataIteratorAdmissionPreservesProgressAndVisibleSlot(t *testing.T) {
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	jobs := map[string]*metadataQueueJob{}
	defer func() {
		for _, job := range jobs {
			if job.scan != nil {
				job.scan.close(t.Context(), context.Canceled)
			}
		}
	}()
	const roots = metadataIteratorLimit + 4
	const records = 140
	for i := range roots {
		dir := t.TempDir()
		for j := range records {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d.jsonl", j)), []byte("unread body"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		jobs[dir] = &metadataQueueJob{target: DirectoryTarget{Path: dir, Scope: "project", WorkspaceRoot: dir, mutationSeq: uint64(i + 1)}}
	}
	now := time.Unix(1, 0)
	visible := ""
	priority := func(target DirectoryTarget) bool { return target.Path == visible }
	started := map[string]*metadataScan{}
	var turn uint64
	run := func(key string, job *metadataQueueJob) {
		t.Helper()
		turn++
		job.turn = turn
		if job.scan == nil {
			if started[key] != nil {
				t.Fatal("time slice restarted a directory")
			}
			job.scan, err = c.startMetadataScan(t.Context(), job.target, job.target.mutationSeq, true)
			if err != nil {
				t.Fatal(err)
			}
			started[key] = job.scan
		} else if started[key] != job.scan {
			t.Fatal("iterator identity changed")
		}
		done, _, err := job.scan.step(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if done {
			job.scan.close(t.Context(), nil)
			count, err := c.CountDirectorySessions(t.Context(), key)
			if err != nil || count != records || !c.DirectoryScanReady(t.Context(), key) {
				t.Fatalf("incomplete discovery published: %d, %v", count, err)
			}
			delete(jobs, key)
		}
		active := 0
		for _, pending := range jobs {
			if pending.scan != nil {
				active++
			}
		}
		if active > metadataIteratorLimit {
			t.Fatalf("iterator budget exceeded: %d", active)
		}
	}
	for range metadataIteratorLimit - 1 {
		key, job := selectMetadataQueueJob(jobs, now, false, priority)
		if job == nil || job.scan != nil {
			t.Fatal("waiting root did not receive its initial slice")
		}
		run(key, job)
	}
	_, job := selectMetadataQueueJob(jobs, now, false, priority)
	if job == nil || job.scan == nil {
		t.Fatal("background discovery consumed the visible-workspace reserve")
	}
	for path, pending := range jobs {
		if pending.scan == nil {
			visible = path
			break
		}
	}
	key, job := selectMetadataQueueJob(jobs, now, true, priority)
	if job == nil || key != visible || job.scan != nil {
		t.Fatal("foreground preparation blocked the visible root's reserved slot")
	}
	run(key, job)
	// Changing the visible workspace while all slots are occupied must not
	// evict a previous iterator or allocate a ninth one.
	for path, pending := range jobs {
		if pending.scan == nil {
			visible = path
			break
		}
	}
	if _, job := selectMetadataQueueJob(jobs, now, true, priority); job != nil {
		t.Fatal("priority change bypassed the iterator budget or resumed P2")
	}
	visible = ""
	for len(jobs) > 0 {
		key, job := selectMetadataQueueJob(jobs, now, false, priority)
		if job == nil {
			t.Fatal("waiting roots could not progress")
		}
		run(key, job)
	}
	if len(started) != roots {
		t.Fatalf("only %d roots completed", len(started))
	}
}

func TestMetadataQueueRotatesBeforeLargeRootCompletes(t *testing.T) {
	large, small := t.TempDir(), t.TempDir()
	for i := range 400 {
		if err := os.WriteFile(filepath.Join(large, fmt.Sprintf("%04d.jsonl", i)), []byte("unreadable body"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(small, "only.jsonl"), []byte("unreadable body"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	_, ok := c.ScheduleReconcile(DirectoryTarget{Path: large, Scope: "global"})
	if !ok {
		t.Fatal("large root rejected")
	}
	done, ok := c.ScheduleReconcile(DirectoryTarget{Path: small, Scope: "global"})
	if !ok {
		t.Fatal("small root rejected")
	}
	c.ResumeDiscovery()
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("small root did not settle")
	}
	if !c.DirectoryScanReady(t.Context(), small) {
		t.Fatal("small root did not publish")
	}
	if c.DirectoryScanReady(t.Context(), large) {
		t.Fatal("small root waited for a complete large scan")
	}
	count, err := c.CountDirectorySessions(t.Context(), large)
	if err != nil || count == 0 || count >= 400 {
		t.Fatalf("large root progress = %d: %v", count, err)
	}
}

func TestMetadataQueueJournalSurvivesRestartWithoutStartingDiscovery(t *testing.T) {
	root, database := t.TempDir(), filepath.Join(t.TempDir(), "catalog.sqlite")
	path := filepath.Join(root, "kept.jsonl")
	if err := os.WriteFile(path, []byte("body must not be decoded"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Path: database, MetadataOnly: true, StartPaused: true}
	c, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	target := DirectoryTarget{Path: root, Scope: "global"}
	if !c.RequestReconcile(target) {
		t.Fatal("request rejected")
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err = Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	var pending int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM catalog_pending_roots`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending=%d: %v", pending, err)
	}
	if _, found, err := c.GetSession(t.Context(), path); err != nil || found {
		t.Fatalf("paused startup scanned source: %v %v", found, err)
	}
	done, ok := c.ScheduleReconcile(target)
	if !ok {
		t.Fatal("resume rejected")
	}
	c.ResumeDiscovery()
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("resumed root did not settle")
	}
	if _, found, err := c.GetSession(t.Context(), path); err != nil || !found {
		t.Fatalf("source missing: %v %v", found, err)
	}
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM catalog_pending_roots`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("completed journal=%d: %v", pending, err)
	}
}
