package main

import (
	"os"
	"testing"
	"time"

	"reasonix/internal/config"
)

func TestRestoreGlobalTopicSessionReindexesProjectTree(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	sessionPath := writeLegacySession(t, dir, "restore-global.jsonl", "restore global history", time.Now().Add(-time.Hour))
	topicID := legacySessionTopicID(sessionPath)
	app := NewApp()

	nodes := waitForCatalogTopic(t, app, "global", "", topicID)
	if len(nodes) != 1 || len(nodes[0].Children) != 1 || nodes[0].Children[0].TopicID != topicID {
		t.Fatalf("legacy session should start in Global, got %#v", nodes)
	}
	// The projected identity is sufficient for an explicit archive/restore;
	// discovery does not need to modify the historical branch metadata first.
	if err := app.TrashTopic(topicID); err != nil {
		t.Fatalf("trash global topic: %v", err)
	}
	ref := assertLegacyLifecycle(t, app, sessionPath, "archived")

	if err := app.RestoreCanonicalSession(ref); err != nil {
		t.Fatalf("restore global session: %v", err)
	}
	if got := app.ListTrashedSessions(); len(got) != 0 {
		t.Fatalf("trash should be empty after restore, got %#v", got)
	}
	nodes = waitForCatalogTopic(t, app, "global", "", topicID)
	if len(nodes) != 1 || nodes[0].Kind != "global_folder" || len(nodes[0].Children) != 1 || nodes[0].Children[0].TopicID != topicID {
		t.Fatalf("restored global session should reappear in Global, got %#v", nodes)
	}
}
