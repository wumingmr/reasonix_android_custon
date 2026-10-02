package main

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/store"
)

func TestDeleteSessionFileMovesLegacyCleanupEvidenceSidecars(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(sessionPath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	active := writeSessionDurableSidecars(t, sessionPath)
	if err := deleteSessionFile(dir, sessionPath); err != nil {
		t.Fatal(err)
	}
	assertSessionSidecarsMissing(t, active)
	assertSessionSidecarsPresent(t, trashedSessionDurableSidecars(dir))
}

func writeSessionDurableSidecars(t *testing.T, sessionPath string) []string {
	t.Helper()
	paths := []string{
		store.SessionTurnEventLog(sessionPath),
		store.SessionTurnEventLogDamaged(sessionPath),
		store.SessionTranscriptProjection(sessionPath),
		store.SessionContext(sessionPath),
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte(`{"fixture":true}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func trashedSessionDurableSidecars(dir string) []string {
	root := filepath.Join(dir, sessionTrashDir, "session.jsonl")
	return []string{
		filepath.Join(root, "session.turns.jsonl"),
		filepath.Join(root, "session.turns.jsonl.damaged"),
		filepath.Join(root, "session.transcript-projection.json"),
		filepath.Join(root, "session.context.json"),
	}
}

func assertSessionSidecarsMissing(t *testing.T, paths []string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("session durable sidecar should be removed from active sessions: %s", path)
		}
	}
}

func assertSessionSidecarsPresent(t *testing.T, paths []string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("session durable sidecar should be in trash: %s: %v", path, err)
		}
	}
}
