package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/projectiondb"
	"reasonix/internal/provider"
)

func appendCompleteMessage(t *testing.T, s *Session, operationID string, message provider.Message) error {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"message": message})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Append(t.Context(), Batch{OperationID: operationID, Events: []Event{{Kind: "message/complete", Payload: payload}}})
	return err
}

func mustAppendCompleteMessage(t *testing.T, s *Session, operationID string, message provider.Message) {
	t.Helper()
	if err := appendCompleteMessage(t, s, operationID, message); err != nil {
		t.Fatalf("append %s: %v", message.ID, err)
	}
	if _, err := s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// A durable write publishes a recovery checkpoint, after which a Service-owned
// runtime holds no message bodies. The writer must still know every live id.
func TestMessageCompleteRefusesIDAlreadyDurable(t *testing.T) {
	reopen := map[string]func(t *testing.T, root string, ref SessionRef){
		"same runtime":                nil,
		"reopened from checkpoint":    func(*testing.T, string, SessionRef) {},
		"reopened without checkpoint": removeRecoveryCheckpoints,
	}
	for name, prepareReopen := range reopen {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "sessions")
			service, err := NewService("local", NewFilesystemPersistence(root))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
			runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "dup-complete-write"})
			if err != nil {
				t.Fatal(err)
			}
			mustAppendCompleteMessage(t, runtime.Session(), "user", provider.Message{ID: "claimed", Role: provider.RoleUser, Content: "the user's request"})
			mustAppendCompleteMessage(t, runtime.Session(), "answer", provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "plan"})
			target := runtime.Session()
			if prepareReopen != nil {
				ref := runtime.Ref()
				if err := service.CloseAll(t.Context()); err != nil {
					t.Fatal(err)
				}
				prepareReopen(t, root, ref)
				service, err = NewService("local", NewFilesystemPersistence(root))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
				opened, err := service.Open(t.Context(), ref)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = opened.Release(context.Background()) })
				target = opened.Runtime().Session()
			}
			err = appendCompleteMessage(t, target, "reused", provider.Message{ID: "claimed", Role: provider.RoleUser, Origin: provider.MessageOriginHost, Content: "plan approved"})
			if !errors.Is(err, ErrDuplicateMessageID) {
				t.Fatalf("append of an id already durable: %v, want ErrDuplicateMessageID", err)
			}
			if err := appendCompleteMessage(t, target, "fresh", provider.Message{ID: "fresh", Role: provider.RoleUser, Content: "next"}); err != nil {
				t.Fatalf("a refused duplicate must not poison later writes: %v", err)
			}
		})
	}
}

func removeRecoveryCheckpoints(t *testing.T, root string, ref SessionRef) {
	t.Helper()
	path := filepath.Join(recoveryCacheDir(filepath.Join(root, ref.SessionID)), recoveryDBName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("recovery checkpoint store: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// commitDuplicateComplete writes the logged shape: a second message/complete
// for an id the log already holds. Forgetting the id reproduces a writer that
// only saw the ids accepted since its last checkpoint.
func commitDuplicateComplete(t *testing.T, s *Session, message provider.Message) {
	t.Helper()
	s.mu.Lock()
	delete(s.messageIDs, message.ID)
	s.mu.Unlock()
	mustAppendCompleteMessage(t, s, "duplicate-"+message.ID, message)
}

func TestDuplicateCompleteOnDiskKeepsFirstOccurrence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "dup-complete-read"})
	if err != nil {
		t.Fatal(err)
	}
	s := runtime.Session()
	mustAppendCompleteMessage(t, s, "user", provider.Message{ID: "claimed", Role: provider.RoleUser, Content: "the user's request"})
	mustAppendCompleteMessage(t, s, "answer", provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "plan"})
	compacted, err := json.Marshal(map[string]any{"messages": []provider.Message{{ID: "answer", Role: provider.RoleAssistant, Content: "plan"}}, "reason": "compaction"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), Batch{OperationID: "compact", Events: []Event{{Kind: "model/context-replace", Payload: compacted}}}); err != nil {
		t.Fatal(err)
	}
	commitDuplicateComplete(t, s, provider.Message{ID: "claimed", Role: provider.RoleUser, Origin: provider.MessageOriginHost, Content: "plan approved"})
	mustAppendCompleteMessage(t, s, "tail", provider.Message{ID: "tail", Role: provider.RoleAssistant, Content: "done"})
	ref := runtime.Ref()
	want := []string{"claimed", "answer", "tail"}
	firstKept := func(where string, messages []provider.Message) {
		t.Helper()
		if got := messageIDs(messages); !idsEqual(got, want) {
			t.Fatalf("%s ids = %v, want %v", where, got, want)
		}
		if messages[0].Content != "the user's request" {
			t.Fatalf("%s kept %q, want the first occurrence", where, messages[0].Content)
		}
	}
	if err := service.CloseAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, removeCaches := range []bool{false, true} {
		if removeCaches {
			for _, cache := range []string{".query-cache", ".recovery-cache"} {
				if err := os.RemoveAll(filepath.Join(root, cache)); err != nil {
					t.Fatal(err)
				}
			}
		}
		reopened, err := NewService("local", NewFilesystemPersistence(root))
		if err != nil {
			t.Fatal(err)
		}
		query := reopened.Query()
		snapshot, err := query.Snapshot(t.Context(), ref)
		if err != nil {
			t.Fatalf("snapshot (caches removed=%v): %v", removeCaches, err)
		}
		firstKept("snapshot", snapshot.Projection.Messages)
		info, err := query.RefreshMetadata(t.Context(), ref)
		if err != nil {
			t.Fatalf("catalog metadata (caches removed=%v): %v", removeCaches, err)
		}
		if info.Preview != "the user's request" {
			t.Fatalf("catalog preview (caches removed=%v) = %q, want the first occurrence", removeCaches, info.Preview)
		}
		if _, _, err := query.prepareHistoryIndex(t.Context(), ref); err != nil {
			t.Fatalf("prepare history index (caches removed=%v): %v", removeCaches, err)
		}
		page, err := query.ReadHistoryWindow(t.Context(), ref, HistoryWindowRequest{Anchor: "newest"})
		if err != nil || page.Status != "ready" {
			t.Fatalf("read history (caches removed=%v): %+v, %v", removeCaches, page, err)
		}
		if got := windowIDs(t, page); !idsEqual(got, want) {
			t.Fatalf("history ids (caches removed=%v) = %v, want %v", removeCaches, got, want)
		}
		if search := searchHistoryReady(t, query, ref, "approved", "", 10); len(search.Hits) != 0 {
			t.Fatalf("search found the discarded occurrence (caches removed=%v): %+v", removeCaches, search)
		}
		opened, err := reopened.Open(t.Context(), ref)
		if err != nil {
			t.Fatalf("reopen (caches removed=%v): %v", removeCaches, err)
		}
		firstKept("reopened", opened.Runtime().Session().Snapshot().Projection.Messages)
		// Without a checkpoint the open replays the whole log, as the first open
		// after an upgrade does.
		if removeCaches {
			if got := opened.Runtime().Session().CatalogMetadata().Preview; got != "the user's request" {
				t.Fatalf("replayed preview = %q, want the first occurrence", got)
			}
			if got := messageIDs(opened.Runtime().Session().DeriveMessages()); !idsEqual(got, []string{"answer", "tail"}) {
				t.Fatalf("replayed model workset = %v, want the compacted context without the repeat", got)
			}
		}
		if err := opened.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := reopened.CloseAll(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

// A checkpoint older than the repeat replays it in the tail. An older build
// demoting the current checkpoint and appending the repeat reaches this.
func TestDuplicateCompleteInCheckpointTailKeepsFirstOccurrence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "dup-complete-tail"})
	if err != nil {
		t.Fatal(err)
	}
	ref := runtime.Ref()
	mustAppendCompleteMessage(t, runtime.Session(), "user", provider.Message{ID: "claimed", Role: provider.RoleUser, Content: "the user's request"})
	mustAppendCompleteMessage(t, runtime.Session(), "answer", provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "plan"})
	if err := service.CloseAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkpoints := filepath.Join(recoveryCacheDir(filepath.Join(root, ref.SessionID)), recoveryDBName)
	stale, err := os.ReadFile(checkpoints)
	if err != nil {
		t.Fatal(err)
	}

	service, err = NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := service.Open(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	commitDuplicateComplete(t, opened.Runtime().Session(), provider.Message{ID: "claimed", Role: provider.RoleUser, Origin: provider.MessageOriginHost, Content: "plan approved"})
	mustAppendCompleteMessage(t, opened.Runtime().Session(), "tail", provider.Message{ID: "tail", Role: provider.RoleAssistant, Content: "done"})
	if err := opened.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.CloseAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpoints, stale, 0o600); err != nil {
		t.Fatal(err)
	}

	service, err = NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	reopened, err := service.Open(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Release(context.Background()) })
	s := reopened.Runtime().Session()
	model := s.DeriveMessages()
	if got := messageIDs(model); !idsEqual(got, []string{"claimed", "answer", "tail"}) || model[0].Content != "the user's request" {
		t.Fatalf("model workset after the checkpoint tail = %v (%q), want the first occurrence only", got, model[0].Content)
	}
	if got := s.CatalogMetadata().Preview; got != "the user's request" {
		t.Fatalf("preview after the checkpoint tail = %q, want the first occurrence", got)
	}
}

// The repeat of an earlier turn's answer must not become a later turn's final
// reply in the history index.
func TestDuplicateCompleteDoesNotBecomeALaterTurnsFinal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "dup-complete-turn"})
	if err != nil {
		t.Fatal(err)
	}
	s := runtime.Session()
	appendTurn := func(turnID string, messages ...provider.Message) {
		t.Helper()
		if _, err := s.Append(t.Context(), Batch{OperationID: turnID + "-start", TurnID: turnID, Events: []Event{{Kind: "turn/start"}}}); err != nil {
			t.Fatal(err)
		}
		for _, message := range messages {
			if message.ID == "answer" && turnID == "second" {
				s.mu.Lock()
				delete(s.messageIDs, message.ID)
				s.mu.Unlock()
			}
			payload, err := json.Marshal(map[string]any{"message": message})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Append(t.Context(), Batch{OperationID: turnID + "-" + message.ID, TurnID: turnID, Events: []Event{{Kind: "message/complete", Payload: payload}}}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Append(t.Context(), Batch{OperationID: turnID + "-end", TurnID: turnID, Events: []Event{{Kind: "turn/end", Payload: []byte(`{"status":"completed"}`)}}}); err != nil {
			t.Fatal(err)
		}
	}
	appendTurn("first", provider.Message{ID: "question", Role: provider.RoleUser, Content: "q"}, provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "a", WorkDurationMs: 1000})
	appendTurn("second", provider.Message{ID: "follow-up", Role: provider.RoleUser, Content: "again"}, provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "repeat", WorkDurationMs: 999999})
	if _, err := s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, path, err := service.Query().prepareHistoryIndex(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := projectiondb.Open(t.Context(), projectiondb.OpenOptions{Path: path, Migrations: historyMigrations, RequireDisk: true, MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.DB.Close()
	var final string
	if err := handle.DB.QueryRowContext(t.Context(), `SELECT COALESCE(final_message_id,'') FROM turn_summaries WHERE turn_id=?`, "second").Scan(&final); err != nil {
		t.Fatal(err)
	}
	if final == "answer" {
		t.Fatalf("the repeated answer became the second turn's final reply")
	}
}
