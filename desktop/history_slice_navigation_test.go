package main

import (
	"fmt"
	"reflect"
	"testing"

	"reasonix/internal/provider"
)

func TestColdHistoryPagesRemainReachableInBothDirections(t *testing.T) {
	a := historySliceTestApp(t)
	tab := newColdHistoryTab(t, a)
	dir := tabSessionDir(tab)
	var messages []provider.Message
	for i := range 20 {
		messages = append(messages, historySliceUser(i, fmt.Sprintf("question %d", i)), historySliceAssistant(i, fmt.Sprintf("answer %d", i)))
	}
	_, tab.SessionPath = saveHistorySliceSession(t, dir, "directions.jsonl", messages)
	h, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer a.ReleaseSessionHistoryRead(h.ID)
	request := HistorySliceRequest{Turns: 2, Entries: 4, Bytes: 1 << 20}
	newest, err := a.ReadSessionHistorySlice(h.ID, request)
	if err != nil || newest.Status != "ready" || !newest.Page.HasOlder {
		t.Fatalf("newest: %+v %v", newest, err)
	}
	request.Cursor = newest.Page.NextCursor
	older, err := a.ReadSessionHistorySlice(h.ID, request)
	if err != nil || older.Status != "ready" || !older.Page.HasNewer {
		t.Fatalf("older: %+v %v", older, err)
	}
	request.Cursor, request.Newer = older.Page.NewerCursor, true
	back, err := a.ReadSessionHistorySlice(h.ID, request)
	if err != nil || back.Status != "ready" {
		t.Fatalf("return newer: %+v %v", back, err)
	}
	if !reflect.DeepEqual(back.Page.Entries, newest.Page.Entries) {
		t.Fatal("reclaimed newer page cannot be reached exactly")
	}
}

func TestColdHistoryCursorBindsStorageSourceBeyondDisplayRevision(t *testing.T) {
	a := historySliceTestApp(t)
	messages := []provider.Message{historySliceUser(0, "one"), historySliceAssistant(0, "answer"), historySliceUser(1, "two"), historySliceAssistant(1, "answer")}
	_, one := saveHistorySliceSession(t, t.TempDir(), "same-name.jsonl", messages)
	_, two := saveHistorySliceSession(t, t.TempDir(), "same-name.jsonl", messages)
	req := normalizeHistorySliceRequest(HistorySliceRequest{Turns: 1, Entries: 2})
	page, ready, err := a.pagedColdHistorySlice(t.Context(), "", one, req)
	if err != nil || !ready || !page.HasOlder {
		t.Fatalf("first: %+v %v", page, err)
	}
	target, ready, err := a.pagedColdHistorySlice(t.Context(), "", two, req)
	if err != nil || !ready {
		t.Fatal(err)
	}
	cursor, err := decodeHistorySliceCursor(page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	// Matching content/revision never authorizes a cursor from another root.
	cursor.Digest, cursor.Revision, cursor.RevKnown = target.Digest, target.Revision, target.RevisionKnown
	req.Cursor = encodeHistorySliceCursor(cursor)
	result, ready, err := a.pagedColdHistorySlice(t.Context(), "", two, req)
	if err != nil || !ready || !result.Stale {
		t.Fatalf("cross-root cursor: %+v %v", result, err)
	}
}
