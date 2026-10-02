package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestNativeHistoryPromptBoundsPreview(t *testing.T) {
	text, err := nativeHistoryPrompt(t.Context(), "\n\t hello   world "+strings.Repeat(" x", 100000))
	if err != nil || !strings.HasPrefix(text, "hello world x") || len(text) > 128 {
		t.Fatalf("preview: %q %v", text, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := nativeHistoryPrompt(ctx, strings.Repeat("\u2003", 100000)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled normalization: %v", err)
	}
}

func TestNativeHistoryColdOutlineAndAnchorsShareCut(t *testing.T) {
	a := historySliceTestApp(t)
	t.Cleanup(a.closeHistoryReaders)
	tab := newColdHistoryTab(t, a)
	var messages []provider.Message
	for i := range 140 {
		messages = append(messages, historySliceUser(i, fmt.Sprintf("question %d", i)), historySliceAssistant(i, fmt.Sprintf("answer %d", i)))
	}
	_, tab.SessionPath = saveHistorySliceSession(t, tabSessionDir(tab), "outline.jsonl", messages)
	before, err := os.ReadFile(tab.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.ReadSessionHistoryOutline(handle.ID, session.HistoryOutlineRequest{Limit: 128})
	if err != nil || first.Status != "ready" || len(first.Entries) != 128 || first.Done || first.NextTurn != 129 || first.TotalTurns != 140 {
		t.Fatalf("first outline: %+v %v", first, err)
	}
	last, err := a.ReadSessionHistoryOutline(handle.ID, session.HistoryOutlineRequest{Generation: first.Generation, SnapshotSequence: &first.SnapshotSequence, StartTurn: first.NextTurn, Limit: 128})
	if err != nil || last.Status != "ready" || len(last.Entries) != 12 || !last.Done || last.Entries[0].Prompt != "question 128" {
		t.Fatalf("last outline: %+v %v", last, err)
	}
	target := first.Entries[3]
	location, err := a.LocateSessionHistoryMessage(handle.ID, target.MessageID, first.SnapshotSequence)
	if err != nil || location.Status != "ready" || location.Position != target.Position || location.VisibleTurn != target.Turn || location.Generation != first.Generation {
		t.Fatalf("locate: %+v %v", location, err)
	}
	for _, request := range []HistorySliceRequest{
		{Cursor: location.Cursor},
		{Anchor: "turn", Turn: target.Turn},
		{Anchor: "message", MessageID: target.MessageID},
	} {
		request.Generation, request.SnapshotSequence, request.Entries = first.Generation, &first.SnapshotSequence, 2
		page, err := a.ReadSessionHistorySlice(handle.ID, request)
		if err != nil || page.Status != "ready" || len(page.Page.Entries) == 0 || page.Page.Entries[len(page.Page.Entries)-1].EntryID != target.MessageID {
			t.Fatalf("target page: %+v %v", page, err)
		}
		if !page.Page.HasNewer || page.Page.NewerCursor == "" {
			t.Fatal("anchored window lost access to the newer suffix")
		}
	}
	for _, id := range []string{target.MessageID + "junk", "sother:r0:m6:o0", "soutline:r999:m6:o0", "soutline:r0:m9999:o0"} {
		location, err := a.LocateSessionHistoryMessage(handle.ID, id, first.SnapshotSequence)
		if err != nil || location.Status != "not_found" {
			t.Fatalf("invalid identity %q: %+v %v", id, location, err)
		}
	}
	if tab.Ctrl != nil {
		t.Fatal("navigation constructed an execution controller")
	}
	after, _ := os.ReadFile(tab.SessionPath)
	if string(before) != string(after) {
		t.Fatal("navigation changed authoritative history")
	}
	wrong := first.SnapshotSequence + 1
	for _, request := range []session.HistoryOutlineRequest{{Generation: "other"}, {SnapshotSequence: &wrong}} {
		page, err := a.ReadSessionHistoryOutline(handle.ID, request)
		if err != nil || page.Status != "stale_cursor" || len(page.Entries) != 0 {
			t.Fatalf("outline accepted another cut: %+v %v", page, err)
		}
	}
	stale, err := a.ReadSessionHistorySlice(handle.ID, HistorySliceRequest{Anchor: "turn", Turn: 4, Generation: "other"})
	if err != nil || stale.Status != "stale_cursor" || len(stale.Page.Entries) != 0 {
		t.Fatalf("jump accepted another cut: %+v %v", stale, err)
	}
	missing, err := a.ReadSessionHistorySlice(handle.ID, HistorySliceRequest{Anchor: "turn", Turn: 999})
	if err != nil || missing.Status != "not_found" {
		t.Fatalf("missing turn: %+v %v", missing, err)
	}
	// A retained binding must detect replacement even before a new Begin call.
	if err := os.WriteFile(tab.SessionPath, []byte("{\"role\":\"user\",\"content\":\"replacement\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := a.ReadSessionHistoryOutline(handle.ID, session.HistoryOutlineRequest{})
	if err != nil || changed.Status != "stale_cursor" || len(changed.Entries) != 0 {
		t.Fatalf("replaced outline: %+v %v", changed, err)
	}
	location, err = a.LocateSessionHistoryMessage(handle.ID, target.MessageID, first.SnapshotSequence)
	if err != nil || location.Status != "stale_cursor" {
		t.Fatalf("replaced locate: %+v %v", location, err)
	}
}

func TestNativeHistoryCanceledOutlineCannotPublish(t *testing.T) {
	a := historySliceTestApp(t)
	t.Cleanup(a.closeHistoryReaders)
	tab := newColdHistoryTab(t, a)
	_, tab.SessionPath = saveHistorySliceSession(t, tabSessionDir(tab), "cancel-outline.jsonl", []provider.Message{historySliceUser(0, "one")})
	resume, err := a.historyMaintenance.Foreground(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	handle, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := a.historyReader(handle.ID)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan session.HistoryOutlinePage, 1)
	go func() {
		page, _ := a.readNativeHistoryOutline(reader, session.HistoryOutlineRequest{})
		result <- page
	}()
	a.ReleaseSessionHistoryRead(handle.ID)
	select {
	case page := <-result:
		if page.Status != "stale_cursor" || len(page.Entries) != 0 {
			t.Fatalf("canceled outline published: %+v", page)
		}
	case <-t.Context().Done():
		t.Fatal(context.Cause(t.Context()))
	}
}
