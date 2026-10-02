package sessioncatalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetadataRefreshDoesNotRewriteDiscoveredTopics(t *testing.T) {
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	_, err = c.db.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<1000)
 INSERT INTO catalog_sessions(path,path_key,directory,directory_key,scope,topic_id,topic_title,ordinary_visible)
 SELECT printf('/old/%d.jsonl',i),printf('/old/%d.jsonl',i),'/old','/old','global',printf('source-%d',i),'Old',1 FROM n;
 INSERT INTO catalog_topics(scope,workspace_root,workspace_root_key,topic_id,title)
 SELECT scope,workspace_root,workspace_root_key,topic_id,topic_title FROM catalog_sessions;
 CREATE TABLE metadata_writes(topic_id TEXT);
 CREATE TRIGGER record_metadata_write AFTER UPDATE ON catalog_topics BEGIN INSERT INTO metadata_writes VALUES(NEW.topic_id); END;`)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := c.SyncMetadata(t.Context(), nil, []TopicMetadata{{Scope: "global", TopicID: "registry", Title: "Named"}}); err != nil {
			t.Fatal(err)
		}
	}
	var writes, sources int
	if err := c.db.QueryRow(`SELECT count(*) FROM metadata_writes WHERE topic_id LIKE 'source-%'`).Scan(&writes); err != nil || writes != 0 {
		t.Fatalf("metadata refresh rewrote unrelated history: %d %v", writes, err)
	}
	if err := c.db.QueryRow(`SELECT count(*) FROM catalog_topics WHERE topic_id LIKE 'source-%'`).Scan(&sources); err != nil || sources != 1000 {
		t.Fatalf("source projection changed: %d %v", sources, err)
	}
	rows, err := c.db.Query(`EXPLAIN QUERY PLAN ` + registeredMetadataTopics)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	usedIndex := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		usedIndex = usedIndex || strings.Contains(detail, "idx_catalog_topics_registered_metadata")
	}
	if err := rows.Err(); err != nil || !usedIndex {
		t.Fatalf("metadata membership scanned all history: %v", err)
	}
}

func TestMetadataRefreshLeavesSourceRetirementToMutationOwner(t *testing.T) {
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(path, []byte("body is not metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.ReconcileDirectory(t.Context(), DirectoryTarget{Path: dir, Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	record, exists, err := c.GetSession(t.Context(), path)
	if err != nil || !exists {
		t.Fatalf("source missing: %v %v", exists, err)
	}
	if err := c.SyncMetadata(t.Context(), nil, []TopicMetadata{{Scope: "global", TopicID: record.TopicID, Title: "Registered"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.SyncMetadata(t.Context(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := c.GetTopic(t.Context(), TopicKey{Scope: "global", TopicID: record.TopicID}); err != nil || !exists {
		t.Fatalf("retiring registry membership lost a live source: %v %v", exists, err)
	}
	if err := c.RemoveSession(t.Context(), path, "fixture"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := c.db.QueryRow(`SELECT count(*) FROM catalog_topics WHERE topic_id=?`, record.TopicID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("source removal left an orphan: %d %v", count, err)
	}
}

func TestMetadataRefreshUsesOldWritersFlagsAndRollsBackFailedSlice(t *testing.T) {
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	if err := c.SyncMetadata(t.Context(), nil, []TopicMetadata{{Scope: "global", TopicID: "keep", Title: "Saved"}}); err != nil {
		t.Fatal(err)
	}
	_, err = c.db.Exec(`INSERT INTO catalog_topics(scope,workspace_root,workspace_root_key,topic_id,title,metadata_present)
 VALUES('global','','','older-writer','Old writer',1);
 CREATE TRIGGER reject_metadata BEFORE INSERT ON catalog_topics WHEN NEW.topic_id='reject' BEGIN SELECT RAISE(ABORT,'fixture rejection'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SyncMetadata(t.Context(), nil, []TopicMetadata{{Scope: "global", TopicID: "reject"}}); err == nil {
		t.Fatal("failed refresh committed")
	}
	var registered int
	if err := c.db.QueryRow(`SELECT count(*) FROM catalog_topics WHERE metadata_present=1`).Scan(&registered); err != nil || registered != 2 {
		t.Fatalf("failed refresh lost prior membership: %d %v", registered, err)
	}
	if err := c.SyncMetadata(t.Context(), nil, []TopicMetadata{{Scope: "global", TopicID: "keep", Title: "Updated"}}); err != nil {
		t.Fatal(err)
	}
	var retired int
	if err := c.db.QueryRow(`SELECT count(*) FROM catalog_topics WHERE topic_id='older-writer'`).Scan(&retired); err != nil || retired != 0 {
		t.Fatalf("older writer row was not retired: %d %v", retired, err)
	}
	// GetTopic deliberately suppresses shells without source rows. Verify the
	// stored registry projection itself, not that unrelated visibility rule.
	var title string
	if err := c.db.QueryRow(`SELECT title FROM catalog_topics WHERE topic_id='keep' AND metadata_present=1`).Scan(&title); err != nil || title != "Updated" {
		t.Fatalf("current metadata missing: %q %v", title, err)
	}
}
