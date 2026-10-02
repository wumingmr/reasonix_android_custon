package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
	"reasonix/internal/provider"
)

func TestRecoveryCheckpointRestoresTailAndIdempotencyWithoutPrefixReplay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions-v4", "fast")
	s, err := CreateWithOptions(dir, "fast", OpenOptions{ExternalHistory: true})
	if err != nil {
		t.Fatal(err)
	}

	for i := range 80 {
		message := provider.Message{ID: "message-" + strings.Repeat("x", 8) + strconv.Itoa(i), Role: provider.RoleUser, Content: strings.Repeat("history", 256)}
		payload, marshalErr := json.Marshal(map[string]any{"message": message})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err := s.Append(context.Background(), Batch{OperationID: "operation-" + string(rune(0x100+i)), Events: []Event{{Kind: "message/complete", Payload: payload}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	wantModel, err := json.Marshal(s.DeriveMessages())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Every query projection is disposable. Removing it must not disable the
	// recovery fast path or recent-message access.
	if err := os.RemoveAll(filepath.Join(filepath.Dir(dir), ".query-cache", "fast")); err != nil {
		t.Fatal(err)
	}
	var stats RecoveryOpenStats
	reopened, err := OpenWithOptions(dir, "fast", OpenOptions{
		ExternalHistory: true,
		ObserveRecovery: func(got RecoveryOpenStats) { stats = got },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })
	if !stats.UsedCheckpoint {
		t.Fatalf("recovery stats = %+v, want checkpoint", stats)
	}
	if stats.LogBytesRead >= stats.LogBytesTotal {
		t.Fatalf("recovery read %d of %d bytes, want bounded tail", stats.LogBytesRead, stats.LogBytesTotal)
	}
	gotModel, err := json.Marshal(reopened.DeriveMessages())
	if err != nil {
		t.Fatal(err)
	}
	if string(gotModel) != string(wantModel) {
		t.Fatal("provider-visible model projection changed across checkpoint recovery")
	}
	recent := reopened.RecentSnapshot()
	if recent.StorageGeneration == "" || recent.DurableSequence != reopened.EventSequence() || len(recent.Entries) == 0 || len(recent.Entries) > RecentMessageLimit {
		t.Fatalf("recent snapshot = %+v", recent)
	}

	// A durable operation remains globally idempotent even though old operation
	// records are no longer resident in Session.operations.
	firstPayload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "message-" + strings.Repeat("x", 8) + "0", Role: provider.RoleUser, Content: strings.Repeat("history", 256)}})
	first, err := reopened.Append(t.Context(), Batch{OperationID: "operation-" + string(rune(0x100)), Events: []Event{{Kind: "message/complete", Payload: firstPayload}}})
	if err != nil {
		t.Fatal(err)
	}
	if first.FirstSequence != 1 {
		t.Fatalf("idempotent commit starts at %d, want 1", first.FirstSequence)
	}
	conflictPayload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "conflict", Role: provider.RoleUser, Content: "different"}})
	if _, err := reopened.Append(t.Context(), Batch{OperationID: "operation-" + string(rune(0x100)), Events: []Event{{Kind: "message/complete", Payload: conflictPayload}}}); err == nil {
		t.Fatal("same operation key with different content unexpectedly succeeded")
	}
}

func TestRecentEntriesKeepAbsoluteVisibleTurns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	messages := []provider.Message{
		{ID: "eight", Role: provider.RoleUser, Content: "eight"},
		{ID: "assistant", Role: provider.RoleAssistant, Content: "answer"},
		{ID: "nine", Role: provider.RoleUser, Content: "nine"},
		{ID: "ten", Role: provider.RoleUser, Content: "ten"},
	}
	entries, err := buildRecentEntries(t.Context(), dir, messages, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{8, 8, 9, 10}
	for index := range entries {
		if entries[index].VisibleTurn != want[index] {
			t.Fatalf("entry %d visible turn=%d, want %d", index, entries[index].VisibleTurn, want[index])
		}
	}
}

func TestRecoveryCheckpointReplaysOnlyDurableTail(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions-v4")
	dir := filepath.Join(root, "tail")
	first, err := CreateWithOptions(dir, "tail", OpenOptions{ExternalHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	appendRecoveryTestMessage(t, first, "one", "first")
	if _, err := first.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Suppress checkpoint publication for one append to simulate a log that is
	// ahead of its last valid recovery point.
	second, err := OpenWithOptions(dir, "tail", OpenOptions{ExternalHistory: true, DisableRecoveryPublish: true})
	if err != nil {
		t.Fatal(err)
	}
	appendRecoveryTestMessage(t, second, "two", "second")
	if _, err := second.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	var stats RecoveryOpenStats
	third, err := OpenWithOptions(dir, "tail", OpenOptions{ExternalHistory: true, ObserveRecovery: func(got RecoveryOpenStats) { stats = got }})
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close(context.Background())
	if !stats.UsedCheckpoint || stats.TailCommits != 1 {
		t.Fatalf("recovery stats = %+v, want one tail commit", stats)
	}
	if got := third.DeriveMessages(); len(got) != 2 || got[0].Content != "first" || got[1].Content != "second" {
		t.Fatalf("tail projection = %#v", got)
	}
}

func TestRecoveryCheckpointFallsBackToPreviousGeneration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions-v4")
	dir := filepath.Join(root, "previous")
	store, err := CreateWithOptions(dir, "previous", OpenOptions{ExternalHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	appendRecoveryTestMessage(t, store, "one", "first")
	if _, err := store.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	appendRecoveryTestMessage(t, store, "two", "second")
	if _, err := store.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(recoveryCacheDir(dir), recoveryDBName)
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(recoveryCheckpointBucket).Put(recoveryCurrentKey, []byte("damaged"))
	}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var stats RecoveryOpenStats
	reopened, err := OpenWithOptions(dir, "previous", OpenOptions{ExternalHistory: true, ObserveRecovery: func(got RecoveryOpenStats) { stats = got }})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	if !stats.UsedCheckpoint {
		t.Fatalf("fallback recovery stats = %+v", stats)
	}
	if got := reopened.DeriveMessages(); len(got) != 2 || got[0].ID != "one" || got[1].ID != "two" {
		t.Fatalf("fallback messages = %+v", got)
	}
}

func appendRecoveryTestMessage(t *testing.T, s *Session, id, content string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"message": provider.Message{ID: id, Role: provider.RoleUser, Content: content}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), Batch{OperationID: "append-" + id, Events: []Event{{Kind: "message/complete", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
}
