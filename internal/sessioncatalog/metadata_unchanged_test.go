package sessioncatalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
)

func TestMetadataUnchangedDiscoveryOnlyUpdatesPresence(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "keep.jsonl"), filepath.Join(dir, "remove.jsonl")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("unread transcript\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	target := DirectoryTarget{Path: dir, Scope: "global"}
	if err := c.ReconcileDirectory(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	_, err = c.db.Exec(`CREATE TABLE metadata_writes(kind TEXT);
 CREATE TRIGGER record_session_write AFTER UPDATE OF preview,turns,topic_id ON catalog_sessions
 BEGIN INSERT INTO metadata_writes VALUES('session'); END;
 CREATE TRIGGER record_topic_write AFTER UPDATE ON catalog_topics
 BEGIN INSERT INTO metadata_writes VALUES('topic'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ReconcileDirectory(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	var writes, absent int
	if err := c.db.QueryRow(`SELECT count(*) FROM metadata_writes`).Scan(&writes); err != nil || writes != 0 {
		t.Fatalf("unchanged discovery rewrote %d projections: %v", writes, err)
	}
	if err := c.db.QueryRow(`SELECT count(*) FROM catalog_sessions s JOIN catalog_directories d ON s.directory_key=d.path_key
 WHERE s.seen_generation<>d.scan_generation OR s.missing_since<>0`).Scan(&absent); err != nil || absent != 0 {
		t.Fatalf("unchanged sources lost scan presence: %d %v", absent, err)
	}
	// A real metadata edit still updates the row and its topic. Removing a
	// sibling must not hide the unchanged source at this scan's EOF.
	if err := agent.SaveBranchMeta(paths[0], agent.BranchMeta{TopicID: "renamed", TopicTitle: "New title", Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths[1]); err != nil {
		t.Fatal(err)
	}
	if err := c.ReconcileDirectory(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	row, found, err := c.GetSession(t.Context(), paths[0])
	if err != nil || !found || row.TopicID != "renamed" || row.MissingSince != 0 {
		t.Fatalf("changed metadata was skipped: %+v %v %v", row, found, err)
	}
	if err := c.db.QueryRow(`SELECT count(*) FROM metadata_writes`).Scan(&writes); err != nil || writes == 0 {
		t.Fatalf("source edit did not update the projection: %d %v", writes, err)
	}
	if err := c.ReconcileDirectory(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	row, found, err = c.GetSession(t.Context(), paths[0])
	if err != nil || !found || row.MissingSince != 0 {
		t.Fatalf("unchanged source vanished after sibling removal: %+v %v %v", row, found, err)
	}
}

func TestMetadataUnchangedComparisonIncludesBranchAndVisibility(t *testing.T) {
	base := SessionRecord{Path: "/history/a.jsonl", Directory: "/history", Scope: "global", TopicID: "topic",
		RecoveryRole: RecoveryRoleNormal, OrdinaryVisible: true, LogFormat: 2, HeadCount: 2, SelectedHeadID: "first"}
	for name, mutate := range map[string]func(*SessionRecord){
		"head":       func(r *SessionRecord) { r.SelectedHeadID = "second" },
		"head count": func(r *SessionRecord) { r.HeadCount = 3 },
		"format":     func(r *SessionRecord) { r.LogFormat = 1 },
		"visibility": func(r *SessionRecord) { r.OrdinaryVisible = false },
		"lineage":    func(r *SessionRecord) { r.RecoveryGroupID = "other" },
		"missing":    func(r *SessionRecord) { r.MissingSince = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if sameMetadataProjection(changed, base) {
				t.Fatal("changed projected identity accepted as unchanged")
			}
		})
	}
}
