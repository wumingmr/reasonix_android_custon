package main

import (
	"fmt"
	"reasonix/internal/provider"
	"sync"
	"testing"
)

func TestHistorySliceConcurrentReadsDuringSave(t *testing.T) {
	app := historySliceTestApp(t)
	dir := t.TempDir()
	var msgs []provider.Message
	for i := range 30 {
		msgs = append(msgs, historySliceToolTurn(i)...)
	}
	sess, path := saveHistorySliceSession(t, dir, "race.jsonl", msgs)
	newLiveHistoryTab(t, app, dir, path, sess)

	const readers = 4
	start := make(chan struct{})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for r := range readers {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			<-start
			cursor := ""
			for {
				select {
				case <-stop:
					return
				default:
				}
				page := app.HistorySliceForTab("test", HistorySliceRequest{Turns: 3, Entries: 25, Cursor: cursor})
				if page.Entries == nil {
					errs <- fmt.Errorf("reader %d: nil entries", r)
					return
				}
				if page.Stale {
					// A save landed between pages: restart from latest, as the
					// frontend would.
					cursor = ""
					continue
				}
				if !page.HasOlder {
					cursor = ""
					continue
				}
				cursor = page.NextCursor
			}
		}(r)
	}

	// Writer: append + save in a loop while readers page.
	close(start)
	for i := 30; i < 38; i++ {
		sess.Add(historySliceUser(i, fmt.Sprintf("q%d", i)))
		sess.Add(historySliceAssistant(i, fmt.Sprintf("a%d", i)))
		if err := sess.Save(path); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("save: %v", err)
		}
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// The final state must page cleanly end to end.
	pages := collectHistorySlicePages(t, app, "test", HistorySliceRequest{Turns: 5, Entries: 40})
	assertPagesMatchReference(t, pages, referenceHistoryRows(t, dir, path))
}
