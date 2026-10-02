package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

func TestHistoryReplacementPreservesMessageVersions(t *testing.T) {
	for _, incremental := range []bool{false, true} {
		name := "rebuild"
		if incremental {
			name = "incremental"
		}
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "sessions")
			service, err := NewService("local", NewFilesystemPersistence(root))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
			runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "replace"})
			if err != nil {
				t.Fatal(err)
			}
			appendWindowMessages(t, runtime, "retained", "removed")
			query, ref := service.Query(), runtime.Ref()
			read := func() HistoryWindowPage {
				t.Helper()
				// Prepare synchronously: the test orders commits and index reads,
				// without depending on a background preparation worker or sleeps.
				if _, _, err := query.prepareHistoryIndex(t.Context(), ref); err != nil {
					t.Fatal(err)
				}
				page, err := query.ReadHistoryWindow(t.Context(), ref, HistoryWindowRequest{Anchor: "newest"})
				if err != nil || page.Status != "ready" {
					t.Fatalf("read history: %+v, %v", page, err)
				}
				return page
			}
			if incremental {
				read()
				searchHistoryReady(t, query, ref, "body", "", 10)
			}
			for i, ids := range [][]string{{"retained"}, {"retained", "removed"}, {"retained", "removed"}} {
				messages := make([]provider.Message, 0, len(ids))
				for _, id := range ids {
					messages = append(messages, provider.Message{ID: id, Role: provider.RoleUser, Content: "rewritten " + id})
				}
				payload, err := json.Marshal(map[string]any{"messages": messages, "reason": "cancel-or-recovery-rewrite", "sourceSequences": []uint64{1}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: fmt.Sprintf("replace-%d", i), Events: []Event{{Kind: "history/replace", Payload: payload}}}); err != nil {
					t.Fatal(err)
				}
				if _, err := runtime.Session().Flush(t.Context()); err != nil {
					t.Fatal(err)
				}
				if incremental || i == 2 {
					page := read()
					if !idsEqual(windowIDs(t, page), ids) {
						t.Fatalf("replacement %d ids: %v", i, windowIDs(t, page))
					}
					if page.Messages[0].Version != i+2 {
						t.Fatalf("retained version = %d, want %d", page.Messages[0].Version, i+2)
					}
					if page := searchHistoryReady(t, query, ref, "rewritten", "", 10); len(page.Hits) != len(ids) {
						t.Fatalf("search replacement %d: %+v", i, page)
					}
				}
			}
			// The same derived index serves the older page API and search.
			if page := historyPageReady(t, query, ref, "", 10); len(page.Messages) != 2 {
				t.Fatalf("history page: %+v", page)
			}
			if page := searchHistoryReady(t, query, ref, "rewritten", "", 10); len(page.Hits) != 2 {
				t.Fatalf("search page: %+v", page)
			}
			if err := service.CloseAll(t.Context()); err != nil {
				t.Fatal(err)
			}
			// Cached and rebuilt readers must both recover the existing durable
			// transcript after restart; no rewrite of user data is needed.
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
				query = reopened.Query()
				if page := read(); len(page.Messages) != 2 || page.Messages[0].Version != 4 {
					t.Fatalf("restart (cache removed=%v): %+v", removeCache, page)
				}
				if err := reopened.CloseAll(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
