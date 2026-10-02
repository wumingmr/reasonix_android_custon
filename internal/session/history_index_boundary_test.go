package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/projectiondb"
)

func historyBoundaryFixture(t *testing.T) (*Service, *Runtime, string, string) {
	t.Helper()
	root := t.TempDir()
	s, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.CloseAll(context.Background()) })
	r, err := s.Create(t.Context(), CreateOptions{SessionID: "boundary"})
	if err != nil {
		t.Fatal(err)
	}
	appendRecoveryTestMessage(t, r.Session(), "first", "first")
	if _, err := r.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s, r, filepath.Join(root, r.Ref().SessionID), historyIndexPath(root, r.Ref().SessionID)
}

func TestHistoryRebuildProgressMatchesScannedTransactions(t *testing.T) {
	s, r, dir, path := historyBoundaryFixture(t)
	// Force the startup ordering: stat, append, then scan. No timing dependency.
	before, err := revisionOfLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	appendRecoveryTestMessage(t, r.Session(), "second", "second")
	if _, err := r.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := rebuildHistoryIndex(t.Context(), dir, path, r.Ref().SessionID, before); err != nil {
		t.Fatal(err)
	}
	page := historyPageReady(t, s.Query(), r.Ref(), "", 100)
	if len(page.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(page.Messages))
	}
}

func TestHistoryRepairsPersistedMismatchedProgress(t *testing.T) {
	s, r, dir, path := historyBoundaryFixture(t)
	before, err := revisionOfLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	appendRecoveryTestMessage(t, r.Session(), "second", "second")
	if _, err := r.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = historyPageReady(t, s.Query(), r.Ref(), "", 100)
	// Reproduce the shipped index: new sequence, old byte offset.
	h, err := projectiondb.Open(t.Context(), projectiondb.OpenOptions{Path: path, Migrations: historyMigrations, RequireDisk: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.DB.ExecContext(t.Context(), "UPDATE metadata SET value=? WHERE key='log_size'", fmt.Sprint(before.Size))
	_ = h.DB.Close()
	if err != nil {
		t.Fatal(err)
	}
	// A new Query proves recovery survives process-local state loss.
	restarted, err := NewService("local", s.persistence)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.CloseAll(context.Background()) })
	page, err := waitHistoryWindow(t, restarted.Query(), r.Ref(), HistoryWindowRequest{Anchor: "newest", Limit: 32})
	if err != nil || len(page.Messages) != 2 {
		t.Fatalf("recovered window: status=%s messages=%d err=%v", page.Status, len(page.Messages), err)
	}
}

func TestHistoryRebuildDoesNotPublishIncompleteTail(t *testing.T) {
	s, r, dir, path := historyBoundaryFixture(t)
	before, err := os.ReadFile(filepath.Join(dir, "events.frames"))
	if err != nil {
		t.Fatal(err)
	}
	appendRecoveryTestMessage(t, r.Session(), "second", "second")
	if _, err := r.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "events.frames")
	complete, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, complete[:len(complete)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	revision, err := revisionOfLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := rebuildHistoryIndex(t.Context(), dir, path, r.Ref().SessionID, revision); err != nil {
		t.Fatal(err)
	}
	h, err := projectiondb.Open(t.Context(), projectiondb.OpenOptions{Path: path, Migrations: historyMigrations, RequireDisk: true})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := readHistoryIndexMetadata(t.Context(), h.DB)
	_ = h.DB.Close()
	if err != nil {
		t.Fatal(err)
	}
	if metadata.logSize != int64(len(before)) {
		t.Fatalf("published offset=%d, last complete transaction ends at %d", metadata.logSize, len(before))
	}
	if err := os.WriteFile(logPath, complete, 0o600); err != nil {
		t.Fatal(err)
	}
	page := historyPageReady(t, s.Query(), r.Ref(), "", 100)
	if len(page.Messages) != 2 {
		t.Fatalf("completed tail messages=%d", len(page.Messages))
	}
}

func TestHistoryRepairDoesNotHideDamagedLog(t *testing.T) {
	s, r, dir, _ := historyBoundaryFixture(t)
	_ = historyPageReady(t, s.Query(), r.Ref(), "", 100)
	log, err := os.OpenFile(filepath.Join(dir, "events.frames"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = log.Write([]byte("invalid frame header"))
	_ = log.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, err = waitHistoryWindow(t, s.Query(), r.Ref(), HistoryWindowRequest{Anchor: "newest"})
	if !errors.Is(err, ErrDamagedStore) {
		t.Fatalf("corruption error=%v", err)
	}
}

func TestHistoryScanStopsAtCapturedSnapshot(t *testing.T) {
	_, r, dir, _ := historyBoundaryFixture(t)
	log, err := os.Open(filepath.Join(dir, "events.frames"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	before, err := log.Stat()
	if err != nil {
		t.Fatal(err)
	}
	visited := 0
	progress, err := scanHistoryLog(t.Context(), log, 0, 1, before.Size(), contentStoreForSessionDir(dir), func(commit Commit) bool {
		visited++
		appendRecoveryTestMessage(t, r.Session(), "during-scan", "during scan")
		if _, err := r.Session().Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if visited != 1 || progress.end != before.Size() || progress.sequence != 1 {
		t.Fatalf("scan followed concurrent append: visited=%d progress=%+v", visited, progress)
	}
}

func TestHistoryScanRejectsReplacedFile(t *testing.T) {
	_, _, dir, _ := historyBoundaryFixture(t)
	path := filepath.Join(dir, "events.frames")
	generation, err := historyProjectionGeneration(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Model the reader retained after a replacement: identical bytes, but a
	// different file identity from the manifest's current log. Windows prevents
	// renaming over an open file, so construct that state without a live rename.
	previousPath := filepath.Join(t.TempDir(), "previous.frames")
	if err := os.WriteFile(previousPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Open(previousPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if err := validateHistoryLog(t.Context(), dir, log, generation); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("replacement accepted: %v", err)
	}
}
