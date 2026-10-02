package session

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reasonix/internal/projectiondb"
	"reasonix/internal/provider"
	"strings"
	"testing"
)

func TestHistoryOutlineWholeHistoryAndFixedCut(t *testing.T) {
	service, err := NewService("local", NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "outline"})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 240)
	for i := range ids {
		ids[i] = fmt.Sprintf("question-%d", i+1)
	}
	appendWindowMessages(t, runtime, ids...)
	query, ref := service.Query(), runtime.Ref()
	if _, _, err := query.prepareHistoryIndex(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	windowReady(t, query, ref, HistoryWindowRequest{Anchor: "newest", Limit: 1})
	first, err := query.ReadHistoryOutline(t.Context(), ref, HistoryOutlineRequest{Limit: 2})
	if err != nil || first.Status != "ready" || first.TotalTurns != 240 || len(first.Entries) != 2 || first.Entries[0].MessageID != ids[0] {
		t.Fatalf("first: %+v, %v", first, err)
	}
	appendWindowMessages(t, runtime, "later")
	if _, _, err := query.prepareHistoryIndex(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	windowReady(t, query, ref, HistoryWindowRequest{Anchor: "newest"})
	last, err := query.ReadHistoryOutline(t.Context(), ref, HistoryOutlineRequest{Generation: first.Generation, SnapshotSequence: &first.SnapshotSequence, StartTurn: 239})
	if err != nil || last.TotalTurns != 240 || len(last.Entries) != 2 || !last.Done || last.Entries[1].MessageID != ids[239] {
		t.Fatalf("last: %+v, %v", last, err)
	}
	stale, err := query.ReadHistoryOutline(t.Context(), ref, HistoryOutlineRequest{Generation: "replaced"})
	if err != nil || stale.Status != "stale_cursor" || stale.Entries == nil {
		t.Fatalf("stale: %+v, %v", stale, err)
	}
}

func TestHistoryOutlinePreviewsVersionsAndCompatibleIndexes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "outline-versions"})
	if err != nil {
		t.Fatal(err)
	}
	write := func(operation string, messages ...provider.Message) {
		t.Helper()
		events := make([]Event, 0, len(messages))
		for _, message := range messages {
			payload, _ := json.Marshal(map[string]any{"message": message})
			events = append(events, Event{Kind: "message/upsert", Payload: payload})
		}
		if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: operation, Events: events}); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.Session().Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := service.Query().prepareHistoryIndex(t.Context(), runtime.Ref()); err != nil {
			t.Fatal(err)
		}
	}
	write("initial", provider.Message{ID: "u1", Role: provider.RoleUser, Content: strings.Repeat("问", 100)},
		provider.Message{ID: "a1", Role: provider.RoleAssistant, Content: "first answer"},
		provider.Message{ID: "a2", Role: provider.RoleAssistant, Content: "last answer"},
		provider.Message{ID: "u2", Role: provider.RoleUser, Content: "second"})
	first, err := service.Query().ReadHistoryOutline(t.Context(), runtime.Ref(), HistoryOutlineRequest{})
	if err != nil || len(first.Entries) != 2 || first.Entries[0].Answer != "last answer" || len([]rune(first.Entries[0].Prompt)) > 50 {
		t.Fatalf("previews: %+v %v", first, err)
	}
	write("edit", provider.Message{ID: "a2", Role: provider.RoleAssistant, Content: "edited answer"})
	old, err := service.Query().ReadHistoryOutline(t.Context(), runtime.Ref(), HistoryOutlineRequest{Generation: first.Generation, SnapshotSequence: &first.SnapshotSequence})
	if err != nil || old.Entries[0].Answer != "last answer" {
		t.Fatalf("fixed version: %+v %v", old, err)
	}
	latest, err := service.Query().ReadHistoryOutline(t.Context(), runtime.Ref(), HistoryOutlineRequest{})
	if err != nil || latest.Entries[0].Answer != "edited answer" {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	// Optional indexes must not advance the schema version older readers support.
	handle, err := projectiondb.Open(t.Context(), projectiondb.OpenOptions{Path: historyIndexPath(root, runtime.Ref().SessionID), Migrations: historyMigrations, RequireDisk: true})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.DB.Close()
	var version int
	if err := handle.DB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 10 {
		t.Fatalf("schema=%d err=%v", version, err)
	}
	for _, query := range []string{
		`SELECT message_id FROM messages WHERE visible_user=1 AND visible_turn>=1 AND event_sequence<=100 AND (valid_to=0 OR valid_to>100) ORDER BY visible_turn,position LIMIT 128`,
		`SELECT preview FROM messages WHERE visible_turn=1 AND role='assistant' AND event_sequence<=100 AND (valid_to=0 OR valid_to>100) ORDER BY position DESC LIMIT 1`,
	} {
		rows, err := handle.DB.Query("EXPLAIN QUERY PLAN " + query)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var a, b, c int
			var detail string
			if err := rows.Scan(&a, &b, &c, &detail); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(detail)
		}
		rows.Close()
		if !strings.Contains(plan.String(), "messages_outline_") || strings.Contains(plan.String(), "SCAN messages") {
			t.Fatalf("unindexed directory query: %s", plan.String())
		}
	}
	// Interrupted additive installation can be retried with no data migration.
	if _, err := handle.DB.Exec(`DROP INDEX messages_outline_answers`); err != nil {
		t.Fatal(err)
	}
	if err := ensureHistoryOutlineIndexes(t.Context(), handle.DB); err != nil {
		t.Fatal(err)
	}
	if err := handle.DB.Close(); err != nil {
		t.Fatal(err)
	}
	for i, ids := range []string{`["u1","a1","a2"]`, `["u2"]`} {
		payload := json.RawMessage(`{"messageIds":` + ids + `}`)
		if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: fmt.Sprintf("retract-%d", i), Events: []Event{{Kind: "message/retract", Payload: payload}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.Session().Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := service.Query().prepareHistoryIndex(t.Context(), runtime.Ref()); err != nil {
			t.Fatal(err)
		}
		page, err := service.Query().ReadHistoryOutline(t.Context(), runtime.Ref(), HistoryOutlineRequest{})
		if err != nil || page.Status != "ready" || page.TotalTurns != 1-i || len(page.Entries) != 1-i || page.Entries == nil {
			t.Fatalf("retraction %d: %+v %v", i, page, err)
		}
		if i == 0 && (page.Entries[0].Turn != 1 || page.Entries[0].MessageID != "u2") {
			t.Fatalf("reindexed identity: %+v", page)
		}
		if i == 1 && !page.Done {
			t.Fatal("empty directory must be complete")
		}
	}
}
