package sessioncatalog

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestTryIndexQueueOverflowDoesNotEnterJournalWriter(t *testing.T) {
	c := &Catalog{opts: Options{MetadataOnly: true}, pathCh: make(chan sessionPathRequest, 1), stop: make(chan struct{})}
	root := t.TempDir()
	target := DirectoryTarget{Path: root, Scope: "global"}
	first := filepath.Join(root, "first.jsonl")
	if !c.TryRequestIndexSession(target, first) {
		t.Fatal("first path rejected")
	}
	// No worker or database is present. Hold the writer boundary to prove a
	// burst can always be consumed even while SQLite publication is blocked.
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	for i := range 4096 {
		if c.TryRequestIndexSession(target, filepath.Join(root, fmt.Sprintf("%d.jsonl", i))) {
			t.Fatal("overflow unexpectedly admitted")
		}
	}
	if !c.TryRequestIndexSession(DirectoryTarget{Path: root, Scope: "project", WorkspaceRoot: root}, first) {
		t.Fatal("queued identity must still coalesce when full")
	}
	count := 0
	c.pathQueued.Range(func(_, value any) bool {
		count++
		if value.(sessionPathRequest).target.Scope != "project" {
			t.Fatal("coalescing lost newest target")
		}
		return true
	})
	if count != 1 || len(c.pathCh) != 1 {
		t.Fatalf("overflow grew the retained queue: paths=%d signals=%d", count, len(c.pathCh))
	}
}
