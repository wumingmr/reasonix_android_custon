package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/store"
)

func awaitNativeSearch(t *testing.T, a *App, id, text, cursor string, limit int) session.SearchHistoryPage {
	t.Helper()
	page, err := a.SearchSessionHistoryRead(id, text, cursor, limit)
	if err != nil {
		t.Fatal(err)
	}
	if page.Status == "preparing" {
		r, readErr := a.historyReader(id)
		if readErr != nil {
			t.Fatal(readErr)
		}
		search := a.prepareNativeHistorySearch(r.native)
		select {
		case <-search.done:
		case <-t.Context().Done():
			t.Fatal("search did not settle")
		}
		page, err = a.SearchSessionHistoryRead(id, text, cursor, limit)
	}
	if err != nil || page.Status != "ready" {
		t.Fatalf("search: %+v %v", page, err)
	}
	return page
}

func TestNativeHistorySearchColdFormatsAndReuse(t *testing.T) {
	for _, kind := range []string{"checkpoint", "schema1"} {
		t.Run(kind, func(t *testing.T) {
			a := historySliceTestApp(t)
			t.Cleanup(a.closeHistoryReaders)
			tab := newColdHistoryTab(t, a)
			messages := []provider.Message{historySliceUser(0, "needle 问题"), historySliceAssistant(0, "second needle 🧭"), historySliceUser(1, "third needle"), historySliceAssistant(1, "literal quote \"needle\"")}
			dir := tabSessionDir(tab)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			tab.SessionPath = filepath.Join(dir, kind+".jsonl")
			var checkpoint []byte
			for _, message := range messages {
				body, _ := json.Marshal(message)
				checkpoint = append(checkpoint, append(body, '\n')...)
			}
			var event []byte
			if kind == "schema1" {
				checkpoint = []byte("{\"role\":\"user\",\"content\":\"obsolete needle\"}\n")
				event, _ = json.Marshal(map[string]any{"schema_version": 1, "type": "replace", "messages": messages})
				if err := os.WriteFile(store.SessionEventLog(tab.SessionPath), event, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(tab.SessionPath, checkpoint, 0600); err != nil {
				t.Fatal(err)
			}
			handle, err := a.BeginSessionHistoryReadForTab(tab.ID)
			if err != nil {
				t.Fatal(err)
			}
			r, _ := a.historyReader(handle.ID)
			if _, err := nativeNavigationPager(r); err != nil {
				t.Fatal(err)
			}
			if r.native.search != nil {
				t.Fatal("ordinary history read prepared full text")
			}
			first := awaitNativeSearch(t, a, handle.ID, "needle", "", 2)
			if len(first.Hits) != 2 || !first.HasMore || first.NextCursor == "" || first.Hits[0].Position != 3 {
				t.Fatalf("first: %+v", first)
			}
			last := awaitNativeSearch(t, a, handle.ID, "needle", first.NextCursor, 2)
			if len(last.Hits) != 2 || last.HasMore || last.Hits[1].Position != 0 {
				t.Fatalf("last: %+v", last)
			}
			for _, hit := range append(first.Hits, last.Hits...) {
				location, err := a.LocateSessionHistoryMessage(handle.ID, hit.MessageID, first.SnapshotSequence)
				if err != nil || location.Status != "ready" || location.Position != hit.Position {
					t.Fatalf("search hit not reachable: %+v %v", location, err)
				}
			}
			for _, text := range []string{"问题", "🧭", `"needle"`} {
				if got := awaitNativeSearch(t, a, handle.ID, text, "", 10); len(got.Hits) != 1 {
					t.Fatalf("Unicode/literal query %q: %+v", text, got)
				}
			}
			if got := awaitNativeSearch(t, a, handle.ID, "obsolete", "", 10); len(got.Hits) != 0 {
				t.Fatal("indexed an obsolete checkpoint")
			}
			for _, cursor := range []string{"garbage", first.NextCursor} {
				page, err := a.SearchSessionHistoryRead(handle.ID, "different query", cursor, 2)
				if err != nil || page.Status != "stale_cursor" || len(page.Hits) != 0 {
					t.Fatalf("accepted foreign cursor: %+v %v", page, err)
				}
			}
			cache := filepath.Join(config.CacheDir(), "history-search-v1", r.native.cacheKey+".sqlite")
			before, err := os.Stat(cache)
			if err != nil {
				t.Fatal(err)
			}
			a.ReleaseSessionHistoryRead(handle.ID)
			<-r.native.closed
			if err := r.native.search.db.Ping(); err == nil {
				t.Fatal("retired search retained its database")
			}
			handle, err = a.BeginSessionHistoryReadForTab(tab.ID)
			if err != nil {
				t.Fatal(err)
			}
			awaitNativeSearch(t, a, handle.ID, "needle", "", 2)
			after, err := os.Stat(cache)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("reopen rebuilt a trusted search cache: %v", err)
			}
			body, _ := os.ReadFile(tab.SessionPath)
			log, _ := os.ReadFile(store.SessionEventLog(tab.SessionPath))
			if string(body) != string(checkpoint) || string(log) != string(event) || tab.Ctrl != nil {
				t.Fatal("cold search changed authoritative history or created a runtime")
			}
			if err := os.WriteFile(tab.SessionPath, []byte("{\"role\":\"user\",\"content\":\"replacement\"}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			stale, err := a.SearchSessionHistoryRead(handle.ID, "needle", "", 2)
			if err != nil || stale.Status != "stale_cursor" || len(stale.Hits) != 0 {
				t.Fatalf("replacement published stale hits: %+v %v", stale, err)
			}
		})
	}
}

func TestNativeHistorySearchSharesCancellationAndCloseBarrier(t *testing.T) {
	a := historySliceTestApp(t)
	t.Cleanup(a.closeHistoryReaders)
	tab := newColdHistoryTab(t, a)
	_, tab.SessionPath = saveHistorySliceSession(t, tabSessionDir(tab), "shared-search.jsonl", []provider.Message{historySliceUser(0, "needle")})
	one, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	two, err := a.BeginSessionHistoryReadForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := a.historyReader(two.ID)
	if _, err := nativeNavigationPager(r); err != nil {
		t.Fatal(err)
	}
	resume, err := a.historyMaintenance.Foreground(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	page, err := a.SearchSessionHistoryRead(one.ID, "needle", "", 1)
	if err != nil || page.Status != "preparing" || page.CoverageSequence != 0 || len(page.Hits) != 0 {
		t.Fatalf("unfinished search claimed complete results: %+v %v", page, err)
	}
	first := a.prepareNativeHistorySearch(r.native)
	a.ReleaseSessionHistoryRead(one.ID)
	if a.prepareNativeHistorySearch(r.native) != first || r.native.ctx.Err() != nil {
		t.Fatal("one reader canceled or replaced its peer's preparation")
	}
	a.ReleaseSessionHistoryRead(two.ID)
	// Search is still held behind the admission gate: retirement must cancel
	// that wait and join it without needing to release an unrelated permit.
	select {
	case <-r.native.closed:
	case <-t.Context().Done():
		t.Fatal("retirement did not cancel the admitted search")
	}
	if !errors.Is(first.err, context.Canceled) || first.db != nil {
		t.Fatalf("canceled preparation published a database: %v", first.err)
	}
}
