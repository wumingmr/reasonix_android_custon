package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"reasonix/internal/event"
)

func TestCompactionPartialCommitThenBudgetFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := New(&fakeProvider{reply: "first durable summary"}, nil, foldableSessionOverForce(8), Options{ContextWindow: 32000}, event.Discard)
		ctx, finish := a.beginCompactionRun(t.Context())
		if err := a.CompactNow(ctx, ""); err != nil {
			t.Fatal(err)
		}
		version, visible := a.currentProjectionVersion(), a.ModelHistorySnapshot()
		if version == 0 {
			t.Fatal("first summary did not commit")
		}
		time.Sleep(4 * time.Minute)
		a.svc.prov = &slowSummaryProvider{}
		_, _, err := a.runSummaryRequest(ctx, a.summaryRequest(visible, ""))
		if err = finish(err); !errors.Is(err, errSummaryBudget) {
			t.Fatalf("error=%v", err)
		}
		if a.currentProjectionVersion() != version || !reflect.DeepEqual(visible, a.ModelHistorySnapshot()) {
			t.Fatal("later failure changed the last committed projection")
		}
	})
}

func TestCompactionQueuedStopNeverIssuesRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &slowSummaryProvider{}
		a := New(p, nil, foldableSessionOverForce(8), Options{ContextWindow: 32000}, event.Discard)
		a.sess.compactionRunMu.Lock()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- a.CompactNow(ctx, "") }()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		a.sess.compactionRunMu.Unlock()
		if p.calls != 0 {
			t.Fatal("cancelled queue entry started a request")
		}
	})
}

func TestBudgetRetainsOwnershipUntilIgnoredCancellationSettles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &lateSummaryProvider{started: make(chan struct{}), release: make(chan struct{})}
		a := New(p, nil, foldableSessionOverForce(8), Options{ContextWindow: 32000}, event.Discard)
		done := make(chan error, 1)
		go func() { done <- a.CompactNow(t.Context(), "") }()
		<-p.started
		time.Sleep(compactionBudget + time.Second)
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("execution ownership released while worker still running")
		default:
		}
		close(p.release)
		if err := <-done; !errors.Is(err, errSummaryBudget) {
			t.Fatal(err)
		}
		if a.currentProjectionVersion() != 0 {
			t.Fatal("late result installed")
		}
	})
}
