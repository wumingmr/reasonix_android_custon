package session

import (
	"context"
	"testing"
	"time"

	"reasonix/internal/projectiondb"
)

func TestObsoleteHistoryPreparesWithoutBlockingAndRefreshesAfterAppend(t *testing.T) {
	s, r, _, path := historyBoundaryFixture(t)
	q := s.Query()
	_ = historyPageReady(t, q, r.Ref(), "", 100)
	h, err := projectiondb.Open(t.Context(), projectiondb.OpenOptions{Path: path, Migrations: historyMigrations, RequireDisk: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.DB.ExecContext(t.Context(), "UPDATE metadata SET value='0' WHERE key='projection_version'")
	_ = h.DB.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Hold both slots: an obsolete on-disk index must return preparing, not
	// rebuild synchronously or wait until another UI action frees capacity.
	q.slots.tryAcquire()
	q.slots.tryAcquire()
	released := false
	defer func() {
		if !released {
			q.slots.release()
			q.slots.release()
		}
	}()
	assertHistoryPreparing(t, q, r.Ref())
	q.slots.release()
	q.slots.release()
	released = true
	page := windowReady(t, q, r.Ref(), HistoryWindowRequest{Anchor: "newest"})
	if len(page.Messages) != 1 {
		t.Fatalf("recovered messages=%d", len(page.Messages))
	}
	// The completed preparation handle must not conceal later invalidation.
	appendRecoveryTestMessage(t, r.Session(), "second", "second")
	if _, err := r.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	page = windowReady(t, q, r.Ref(), HistoryWindowRequest{Anchor: "newest"})
	if len(page.Messages) != 2 {
		t.Fatalf("appended messages=%d", len(page.Messages))
	}
}

func TestHistoryReadersDoNotWaitOnRebuildMutex(t *testing.T) {
	s, r, _, _ := historyBoundaryFixture(t)
	q := s.Query()
	_ = historyPageReady(t, q, r.Ref(), "", 100)
	lock := q.projectionLock("history", r.Ref().SessionID)
	lock.Lock()
	defer lock.Unlock()
	assertHistoryPreparing(t, q, r.Ref())
}

func assertHistoryPreparing(t *testing.T, q *Query, ref SessionRef) {
	t.Helper()
	readers := []func(context.Context) (string, error){
		func(ctx context.Context) (string, error) {
			p, e := q.ReadHistoryWindow(ctx, ref, HistoryWindowRequest{Anchor: "newest"})
			return p.Status, e
		},
		func(ctx context.Context) (string, error) {
			p, e := q.HistoryPage(ctx, ref, "", 100)
			return p.Status, e
		},
		func(ctx context.Context) (string, error) {
			p, e := q.LocateMessage(ctx, ref, "first", 0)
			return p.Status, e
		},
	}
	for i, read := range readers {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		type result struct {
			status string
			err    error
		}
		done := make(chan result, 1)
		go func() { status, err := read(ctx); done <- result{status, err} }()
		select {
		case p := <-done:
			cancel()
			if p.err != nil || p.status != "preparing" {
				t.Fatalf("reader %d: %+v", i, p)
			}
		case <-ctx.Done():
			cancel()
			t.Fatalf("reader %d blocked behind rebuild", i)
		}
	}
}
