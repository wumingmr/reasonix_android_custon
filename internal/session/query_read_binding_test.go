package session

import (
	"context"
	"testing"
)

func TestHistoryReadScopeSharedLifetimeAndNavigationFence(t *testing.T) {
	q := newQuery("local", nil, nil)
	defer q.rebuildStop()
	ref := SessionRef{HostID: "local", SessionID: "history"}
	a, releaseA, err := q.AcquireHistoryReader(ref)
	if err != nil {
		t.Fatal(err)
	}
	b, releaseB, err := q.AcquireHistoryReader(ref)
	if err != nil {
		t.Fatal(err)
	}
	individual, cancel := context.WithCancel(a)
	cancel()
	if q.historyReadContext(ref.SessionID, individual).Err() != nil {
		t.Fatal("one cancelled RPC cancelled shared preparation")
	}
	releaseA()
	releaseA()
	if b.Err() != nil {
		t.Fatal("one reader retired the shared scope")
	}
	releaseB()
	if b.Err() != context.Canceled {
		t.Fatal("last release did not cancel preparation")
	}
	c, releaseC, err := q.AcquireHistoryReader(ref)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseC()
	compatibility := q.historyReadContext(ref.SessionID, context.Background())
	if compatibility != q.rebuildCtx {
		t.Fatal("unbound RPC borrowed another reader's cancellation owner")
	}
	if c.Err() != nil {
		t.Fatal("new navigation inherited the cancelled scope")
	}
	if q.historyReadContext(ref.SessionID, a).Err() != context.Canceled {
		t.Fatal("late old request borrowed the new reader's preparation lifetime")
	}
	if q.historyReadContext(ref.SessionID, c).Err() != nil {
		t.Fatal("old release affected new scope")
	}
}

func TestUnboundPreparationSurvivesCompletedHTTPRequest(t *testing.T) {
	q := newQuery("local", nil, nil)
	defer q.rebuildStop()
	request, finish := context.WithCancel(context.Background())
	owner := q.historyReadContext("history", request)
	finish()
	if owner.Err() != nil {
		t.Fatal("preparing response canceled its asynchronous preparation")
	}
	if q.historyReadContext("history", request).Err() != context.Canceled {
		t.Fatal("already-canceled request was admitted")
	}
}
