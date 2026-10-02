package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
)

func TestLegacyRecoveryMigrationWaitsForParentRemoval(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	parent := writeLegacySession(t, dir, "normal.jsonl", "parent prompt", time.Now().Add(-2*time.Hour))
	recovery := writeLegacySession(t, dir, "normal-recovery-0123456789abcdef.jsonl", "recovery prompt", time.Now().Add(-time.Hour))

	migrated, paths := forceMigrateLegacySessionsIntoGlobalTopicsWithPaths(dir)
	if len(migrated) != 1 || migrated[0] != legacySessionTopicID(parent) || len(paths) != 1 || !sameDesktopPath(paths[0], parent) {
		t.Fatalf("migration with parent present = topics:%v paths:%v", migrated, paths)
	}
	if _, ok, err := agent.LoadBranchMeta(recovery); err != nil || ok {
		t.Fatalf("recovery meta with parent present: ok=%v err=%v", ok, err)
	}

	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	migrated, paths = forceMigrateLegacySessionsIntoGlobalTopicsWithPaths(dir)
	if len(migrated) != 1 || migrated[0] != legacySessionTopicID(recovery) || len(paths) != 1 || !sameDesktopPath(paths[0], recovery) {
		t.Fatalf("migration after parent removal = topics:%v paths:%v", migrated, paths)
	}
	if meta, ok, err := agent.LoadBranchMeta(recovery); err != nil || !ok || meta.TopicID != legacySessionTopicID(recovery) {
		t.Fatalf("recovery meta after parent removal: meta=%+v ok=%v err=%v", meta, ok, err)
	}
	if got := filepath.Base(recovery); got != "normal-recovery-0123456789abcdef.jsonl" {
		t.Fatalf("recovery transcript moved: %q", got)
	}
}
