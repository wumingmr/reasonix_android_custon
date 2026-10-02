package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

func duplicateReplacementPayload(t *testing.T, kind string) json.RawMessage {
	t.Helper()
	messages := []provider.Message{
		{ID: "dup", Role: provider.RoleUser, Content: "first dup"},
		{ID: "answer", Role: provider.RoleAssistant, Content: "plan"},
		{ID: "dup", Role: provider.RoleUser, Content: "second dup"},
		{ID: "tail", Role: provider.RoleAssistant, Content: "done"},
	}
	body := map[string]any{"messages": messages, "reason": "fresh-resume"}
	if kind == "legacy/import" {
		body = map[string]any{"messages": messages, "source": Source{Path: "legacy.jsonl"}}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestReplacementRefusesDuplicateMessageID(t *testing.T) {
	for _, kind := range []string{"history/replace", "legacy/import"} {
		t.Run(kind, func(t *testing.T) {
			service, err := NewService("local", NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
			runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "dup-write"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runtime.Session().Append(t.Context(), Batch{OperationID: "dup", Events: []Event{{Kind: kind, Payload: duplicateReplacementPayload(t, kind)}}})
			if !errors.Is(err, ErrDuplicateMessageID) {
				t.Fatalf("append duplicate %s: %v, want ErrDuplicateMessageID", kind, err)
			}
		})
	}
}

// commitDuplicateReplacement writes a history/replace the writer now refuses,
// standing in for a log committed before that check existed.
func commitDuplicateReplacement(t *testing.T, s *Session) {
	t.Helper()
	valid, err := json.Marshal(map[string]any{"messages": []provider.Message{{ID: "placeholder", Role: provider.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := s.PrepareBatchContext(t.Context(), "dup-on-disk", Batch{Events: []Event{{Kind: "history/replace", Payload: valid}}})
	if err != nil {
		t.Fatal(err)
	}
	payload := duplicateReplacementPayload(t, "history/replace")
	prepared.events[0].Payload = payload
	prepared.storedEvents[0].Payload, prepared.storedEvents[0].PayloadRef = payload, nil
	if _, err := s.CommitPrepared(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateMessageIDOnDiskStillHydrates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "dup-read"})
	if err != nil {
		t.Fatal(err)
	}
	commitDuplicateReplacement(t, runtime.Session())
	want := []string{"dup", "answer", "tail"}
	if got := messageIDs(runtime.Session().Snapshot().Projection.Messages); !idsEqual(got, want) {
		t.Errorf("projection ids = %v, want %v", got, want)
	}
	ref := runtime.Ref()
	if err := service.CloseAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, removeCache := range []bool{false, true} {
		if removeCache {
			if err := os.RemoveAll(filepath.Join(root, ".query-cache")); err != nil {
				t.Fatal(err)
			}
		}
		reopened, err := NewService("local", NewFilesystemPersistence(root))
		if err != nil {
			t.Fatal(err)
		}
		query := reopened.Query()
		if _, _, err := query.prepareHistoryIndex(t.Context(), ref); err != nil {
			t.Fatalf("prepare history index (cache removed=%v): %v", removeCache, err)
		}
		page, err := query.ReadHistoryWindow(t.Context(), ref, HistoryWindowRequest{Anchor: "newest"})
		if err != nil || page.Status != "ready" {
			t.Fatalf("read history (cache removed=%v): %+v, %v", removeCache, page, err)
		}
		if got := windowIDs(t, page); !idsEqual(got, want) {
			t.Fatalf("history ids (cache removed=%v) = %v, want %v", removeCache, got, want)
		}
		if search := searchHistoryReady(t, query, ref, "dup", "", 10); len(search.Hits) != 1 {
			t.Fatalf("search (cache removed=%v): %+v", removeCache, search)
		}
		opened, err := reopened.Open(t.Context(), ref)
		if err != nil {
			t.Fatalf("reopen (cache removed=%v): %v", removeCache, err)
		}
		if got := messageIDs(opened.Runtime().Session().Snapshot().Projection.Messages); !idsEqual(got, want) {
			t.Fatalf("reopened projection ids = %v, want %v", got, want)
		}
		if err := opened.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := reopened.CloseAll(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func messageIDs(messages []provider.Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	return ids
}
