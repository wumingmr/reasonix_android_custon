package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/store"
)

// TestHistorySliceColdLegacyEventFormat pages an ancient event-record
// session through the streaming legacy fallback.
func TestHistorySliceColdLegacyEventFormat(t *testing.T) {
	app := historySliceTestApp(t)
	tab := newColdHistoryTab(t, app)
	dir := tabSessionDir(tab)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "legacy.jsonl")
	var body strings.Builder
	for i := range 10 {
		fmt.Fprintf(&body, "{\"kind\":\"user.message\",\"text\":\"question %d\"}\n", i)
		fmt.Fprintf(&body, "{\"kind\":\"model.final\",\"content\":\"answer %d\"}\n", i)
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	tab.SessionPath = path

	pages := collectHistorySlicePages(t, app, "cold", HistorySliceRequest{Turns: 4, Entries: 6})
	rows := concatHistoryPages(pages)
	if len(rows) != 20 {
		t.Fatalf("legacy rows = %d, want 20", len(rows))
	}
	if rows[0].Role != "user" || rows[0].Content != "question 0" {
		t.Fatalf("first legacy row = %+v", rows[0])
	}
	if rows[19].Content != "answer 9" {
		t.Fatalf("last legacy row = %+v", rows[19])
	}
	if pages[0].TotalTurns != 10 {
		t.Fatalf("TotalTurns = %d, want 10", pages[0].TotalTurns)
	}
	// No display index may be written for the legacy format.
	if _, err := os.Stat(store.SessionDisplayIndex(path)); !os.IsNotExist(err) {
		t.Fatal("legacy event-format sessions must not get a display index")
	}
}
