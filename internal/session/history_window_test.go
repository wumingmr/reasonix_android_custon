package session

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/provider"
)

func windowReady(t *testing.T, query *Query, ref SessionRef, req HistoryWindowRequest) HistoryWindowPage {
	t.Helper()
	page, err := waitHistoryWindow(t, query, ref, req)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func waitHistoryWindow(t *testing.T, query *Query, ref SessionRef, req HistoryWindowRequest) (HistoryWindowPage, error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		page, err := query.ReadHistoryWindow(t.Context(), ref, req)
		if err != nil || page.Status != "preparing" {
			return page, err
		}
		if time.Now().After(deadline) {
			t.Fatalf("window stayed preparing: %+v", page)
		}
		time.Sleep(time.Millisecond)
	}
}

func appendWindowMessages(t *testing.T, runtime *Runtime, ids ...string) {
	t.Helper()
	for _, id := range ids {
		payload, err := json.Marshal(map[string]any{"message": provider.Message{ID: id, Role: provider.RoleUser, Content: "body-" + id}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: "message-" + id, Events: []Event{{Kind: "message/complete", Payload: payload}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func windowIDs(t *testing.T, page HistoryWindowPage) []string {
	t.Helper()
	ids := make([]string, 0, len(page.Messages))
	for _, m := range page.Messages {
		ids = append(ids, m.MessageID)
	}
	return ids
}

func idsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHistoryWindowNewestAndOlderPaging(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions-v4")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "windowed"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background(), runtime.Ref()) })
	appendWindowMessages(t, runtime, "m1", "m2", "m3", "m4", "m5")
	ref := runtime.Ref()
	query := service.Query()

	newest := windowReady(t, query, ref, HistoryWindowRequest{Anchor: "newest", Limit: 2})
	if newest.Status != "ready" || !idsEqual(windowIDs(t, newest), []string{"m4", "m5"}) {
		t.Fatalf("newest page = %v", windowIDs(t, newest))
	}
	if !newest.HasOlder || newest.OlderCursor == "" || newest.HasNewer {
		t.Fatalf("newest page cursors = %+v", newest)
	}
	older := windowReady(t, query, ref, HistoryWindowRequest{Anchor: "cursor", Cursor: newest.OlderCursor, Limit: 2})
	if !idsEqual(windowIDs(t, older), []string{"m2", "m3"}) {
		t.Fatalf("older page = %v", windowIDs(t, older))
	}
	if !older.HasNewer || older.NewerCursor == "" {
		t.Fatalf("older page must expose a newer cursor: %+v", older)
	}
	newer := windowReady(t, query, ref, HistoryWindowRequest{Anchor: "cursor", Cursor: older.NewerCursor, Limit: 5})
	if !idsEqual(windowIDs(t, newer), []string{"m4", "m5"}) {
		t.Fatalf("newer continuation = %v", windowIDs(t, newer))
	}
	oldest := windowReady(t, query, ref, HistoryWindowRequest{Anchor: "cursor", Cursor: older.OlderCursor, Limit: 5})
	if !idsEqual(windowIDs(t, oldest), []string{"m1"}) || oldest.HasOlder {
		t.Fatalf("oldest page = %v hasOlder=%v", windowIDs(t, oldest), oldest.HasOlder)
	}
}

func TestHistoryWindowMessageAndTurnAnchors(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions-v4")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "anchored"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background(), runtime.Ref()) })
	appendWindowMessages(t, runtime, "a1", "a2", "a3", "a4")
	ref := runtime.Ref()
	query := service.Query()

	// A search-hit-style jump lands a window around the target without
	// walking from the newest page.
	byMessage := windowReady(t, query, ref, HistoryWindowRequest{Anchor: "message", MessageID: "a2", Limit: 2})
	if !idsEqual(windowIDs(t, byMessage), []string{"a1", "a2"}) {
		t.Fatalf("message anchor page = %v", windowIDs(t, byMessage))
	}
	if !byMessage.HasNewer || byMessage.NewerCursor == "" {
		t.Fatalf("message anchor must continue newer: %+v", byMessage)
	}
	afterTarget := windowReady(t, query, ref, HistoryWindowRequest{Anchor: "cursor", Cursor: byMessage.NewerCursor, Limit: 2})
	if !idsEqual(windowIDs(t, afterTarget), []string{"a3", "a4"}) {
		t.Fatalf("newer continuation = %v", windowIDs(t, afterTarget))
	}

	byTurn := windowReady(t, query, ref, HistoryWindowRequest{Anchor: "turn", Turn: 4, Limit: 1})
	if len(byTurn.Messages) != 1 || byTurn.Messages[0].MessageID != "a4" {
		t.Fatalf("turn anchor page = %+v", byTurn)
	}
	missing := windowReady(t, query, ref, HistoryWindowRequest{Anchor: "message", MessageID: "absent", Limit: 2})
	if missing.Status != "not_found" {
		t.Fatalf("missing anchor status = %s", missing.Status)
	}
}

func mustEncodeWindowCursor(t *testing.T, c historyWindowCursor) string {
	t.Helper()
	cursor, err := encodeHistoryWindowCursor(c)
	if err != nil {
		t.Fatal(err)
	}
	return cursor
}

func TestHistoryWindowRejectsForeignCursor(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions-v4")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "fenced"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background(), runtime.Ref()) })
	appendWindowMessages(t, runtime, "f1", "f2")
	ref := runtime.Ref()
	page := windowReady(t, service.Query(), ref, HistoryWindowRequest{Anchor: "newest", Limit: 1})
	if page.OlderCursor == "" {
		t.Fatal("expected an older cursor")
	}
	parsed, err := decodeHistoryWindowCursor(page.OlderCursor)
	if err != nil {
		t.Fatal(err)
	}
	// A cursor whose storage generation no longer matches — as after a
	// storage replacement or projection rebuild — answers stale_cursor.
	foreign := parsed
	foreign.Generation = "g-none"
	stale, err := service.Query().ReadHistoryWindow(t.Context(), ref, HistoryWindowRequest{Anchor: "cursor", Cursor: mustEncodeWindowCursor(t, foreign), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != "stale_cursor" {
		t.Fatalf("foreign generation cursor = %s, want stale_cursor", stale.Status)
	}
	foreign = parsed
	foreign.SnapshotSequence += 1 << 40
	overrun, err := service.Query().ReadHistoryWindow(t.Context(), ref, HistoryWindowRequest{Anchor: "cursor", Cursor: mustEncodeWindowCursor(t, foreign), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if overrun.Status != "stale_cursor" {
		t.Fatalf("future snapshot cursor = %s, want stale_cursor", overrun.Status)
	}
}

func TestReadMessageFieldReturnsBoundedAlignedFragments(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions-v4")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "fields"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background(), runtime.Ref()) })
	content := strings.Repeat("汉", 30000) + `quote " and \backslash` + strings.Repeat("字", 3000)
	payload, err := json.Marshal(map[string]any{"message": provider.Message{ID: "big", Role: provider.RoleUser, Content: content}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: "message-big", Events: []Event{{Kind: "message/complete", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	ref := runtime.Ref()
	query := service.Query()
	// Display the page once so the referenced content gains its range grant.
	page := historyPageReady(t, query, ref, "", 10)
	if len(page.Messages) != 1 || page.Messages[0].ContentRef == nil {
		t.Fatalf("expected a referenced large message, got %+v", page)
	}

	first, err := query.ReadMessageField(t.Context(), ref, "big", 0, "content", 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "ready" || first.TotalBytes == 0 || len(first.Data) == 0 || first.NextOffset == 0 {
		t.Fatalf("first fragment = status:%s total:%d data:%d next:%d", first.Status, first.TotalBytes, len(first.Data), first.NextOffset)
	}
	var assembled []byte
	assembled = append(assembled, first.Data...)
	offset := first.NextOffset
	for offset != 0 {
		frag, err := query.ReadMessageField(t.Context(), ref, "big", 0, "content", offset, 256<<10)
		if err != nil {
			t.Fatal(err)
		}
		assembled = append(assembled, frag.Data...)
		offset = frag.NextOffset
	}
	var decoded string
	if err := json.Unmarshal(assembled, &decoded); err != nil {
		t.Fatalf("reassembled field is not the original JSON string: %v", err)
	}
	if decoded != content {
		t.Fatal("reassembled content mismatch")
	}
	whole, err := query.ReadMessageField(t.Context(), ref, "big", 0, "canonicalMessage", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var message provider.Message
	if err := json.Unmarshal(whole.Data, &message); err != nil {
		t.Fatalf("canonicalMessage fragment did not parse: %v", err)
	}
	absent, err := query.ReadMessageField(t.Context(), ref, "absent", 0, "content", 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	if absent.Status != "not_found" {
		t.Fatalf("unknown message status = %s, want not_found", absent.Status)
	}
	if _, err := query.ReadMessageField(t.Context(), ref, "big", 0, "", 0, 64); err == nil {
		t.Fatal("field read without a field name succeeded")
	}
	if _, err := query.ReadMessageField(t.Context(), ref, "big", 0, "content", -1, 64); err == nil {
		t.Fatal("negative offset accepted")
	}
}
