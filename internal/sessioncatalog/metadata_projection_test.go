package sessioncatalog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"reasonix/internal/historywork"
)

func TestMetadataIncrementalWaitingObservationCanCancel(t *testing.T) {
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once, unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }); _ = c.Close(context.Background()) })
	c.testMetadataSliceHook = func(int) { once.Do(func() { close(entered); <-release }) }
	completed := make(chan error, 1)
	go func() { completed <- c.SyncMetadata(t.Context(), nil, nil) }()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.SyncMetadata(ctx, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting observation ignored cancellation: %v", err)
	}
	unblock.Do(func() { close(release) })
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
}

func TestMetadataIncrementalSkipsUnchangedRegistryWrites(t *testing.T) {
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	projects := []ProjectRecord{{Scope: "global", Title: "Global"}}
	topics := []TopicMetadata{{Scope: "global", TopicID: "kept", Title: "Named", Pinned: true, CreatedAt: 42}}
	if err := c.SyncMetadata(t.Context(), projects, topics); err != nil {
		t.Fatal(err)
	}
	_, err = c.db.Exec(`CREATE TABLE registry_writes(kind TEXT);
 CREATE TRIGGER watch_projects AFTER UPDATE ON catalog_projects BEGIN INSERT INTO registry_writes VALUES('project'); END;
 CREATE TRIGGER watch_topics AFTER UPDATE ON catalog_topics BEGIN INSERT INTO registry_writes VALUES('topic'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	revision := c.revision.Load()
	if err := c.SyncMetadata(t.Context(), projects, topics); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := c.db.QueryRow(`SELECT count(*) FROM registry_writes`).Scan(&count); err != nil || count != 0 || revision != c.revision.Load() {
		t.Fatalf("unchanged registry republished: writes=%d revision=%d/%d err=%v", count, revision, c.revision.Load(), err)
	}
	// The current database, including an older writer's update, must be checked
	// again. A remembered input hash would incorrectly skip this correction.
	if _, err := c.db.Exec(`UPDATE catalog_topics SET title='other writer' WHERE topic_id='kept'`); err != nil {
		t.Fatal(err)
	}
	if err := c.SyncMetadata(t.Context(), projects, topics); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := c.db.QueryRow(`SELECT title FROM catalog_topics WHERE topic_id='kept'`).Scan(&title); err != nil || title != "Named" {
		t.Fatalf("old writer bypassed projection validation: %q %v", title, err)
	}
}

func TestMetadataIncrementalCancellationKeepsUnvisitedMembership(t *testing.T) {
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	if err := c.SyncMetadata(t.Context(), nil, []TopicMetadata{{Scope: "global", TopicID: "retire-after-success", Title: "Old"}}); err != nil {
		t.Fatal(err)
	}
	topics := make([]TopicMetadata, 3*historywork.BatchEntries)
	for i := range topics {
		topics[i] = TopicMetadata{Scope: "global", TopicID: fmt.Sprintf("new-%04d", i), Title: "New"}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := 0
	c.testMetadataSliceHook = func(entries int) {
		observed += entries
		if entries > historywork.BatchEntries {
			t.Fatalf("unbounded registry slice: %d", entries)
		}
		if !c.mutationMu.TryLock() {
			t.Fatal("registry kept the writer between slices")
		}
		c.mutationMu.Unlock()
		cancel()
	}
	if err := c.SyncMetadata(ctx, nil, topics); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
	if observed == 0 || observed > historywork.BatchEntries {
		t.Fatalf("cancellation restarted or consumed unvisited input: %d", observed)
	}
	var retained int
	if err := c.db.QueryRow(`SELECT count(*) FROM catalog_topics WHERE topic_id='retire-after-success' AND metadata_present=1`).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("partial refresh retired unvisited membership: %d %v", retained, err)
	}
	c.testMetadataSliceHook = nil
	if err := c.SyncMetadata(t.Context(), nil, topics); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := c.db.QueryRow(`SELECT count(*) FROM catalog_topics WHERE metadata_present=1`).Scan(&count); err != nil || count != len(topics) {
		t.Fatalf("retry lost registry input: %d %v", count, err)
	}
	if err := c.db.QueryRow(`SELECT count(*) FROM catalog_topics WHERE topic_id='retire-after-success'`).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("completed refresh did not retire orphan: %d %v", retained, err)
	}
}

func TestMetadataIncrementalAllowsSourceCommitBetweenSlices(t *testing.T) {
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	topics := make([]TopicMetadata, 2*historywork.BatchEntries)
	for i := range topics {
		topics[i] = TopicMetadata{Scope: "global", TopicID: fmt.Sprintf("topic-%04d", i), Title: "Registered"}
	}
	committed := false
	c.testMetadataSliceHook = func(int) {
		if committed {
			return
		}
		committed = true
		if !c.mutationMu.TryLock() {
			t.Fatal("source commit cannot acquire the writer")
		}
		c.mutationMu.Unlock()
		err := c.UpsertSession(t.Context(), SessionRecord{Path: "/fixture/saved.jsonl", Directory: "/fixture", Scope: "global",
			TopicID: "source-only", TopicTitle: "Concurrent source", Turns: 4, TurnsState: TurnsValid, Health: HealthOK})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := c.SyncMetadata(t.Context(), nil, topics); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := c.db.QueryRow(`SELECT count(*) FROM catalog_topics WHERE topic_id='source-only'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("registry retirement removed concurrent source: %d %v", count, err)
	}
}
