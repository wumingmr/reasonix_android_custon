package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogMetadataRefreshCoalescesAndJoinsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan int, 2)
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	request, done := startCatalogMetadataRefresh(ctx, func(ctx context.Context) {
		call := int(calls.Add(1))
		started <- call
		if call == 1 {
			select {
			case <-releaseFirst:
			case <-ctx.Done():
			}
		} else {
			<-ctx.Done()
		}
	})
	waitStart := func(want int) {
		t.Helper()
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("refresh order %d, want %d", got, want)
			}
		case <-time.After(sessionCatalogTestDeadline):
			t.Fatal("refresh did not start")
		}
	}
	request()
	waitStart(1)
	// These requests return while the writer remains blocked. They retain one
	// trailing refresh so a change arriving during a refresh cannot be lost.
	for range 1000 {
		request()
	}
	if calls.Load() != 1 {
		t.Fatal("metadata refreshes overlapped")
	}
	close(releaseFirst)
	waitStart(2)
	request()
	cancel()
	select {
	case <-done:
	case <-time.After(sessionCatalogTestDeadline):
		t.Fatal("metadata worker did not join cancellation")
	}
	request()
	if calls.Load() != 2 {
		t.Fatal("pending refresh ran after cancellation")
	}
}

func TestCatalogMetadataRequestsRetainOneWakeupAndUseCurrentLifecycle(t *testing.T) {
	app := NewApp()
	old := make(chan struct{}, 1)
	app.catalogMetadataRequests = old
	for range 1000 {
		app.requestSessionCatalogMetadataSync()
	}
	if len(old) != 1 {
		t.Fatal("metadata requests did not coalesce")
	}
	<-old
	current := make(chan struct{}, 1)
	app.catalogLifecycleMu.Lock()
	app.catalogMetadataRequests = current
	app.catalogLifecycleMu.Unlock()
	app.requestSessionCatalogMetadataSync()
	if len(old) != 0 || len(current) != 1 {
		t.Fatal("metadata edit reached an old catalog lifecycle")
	}
	<-current
	app.catalogLifecycleMu.Lock()
	app.catalogMetadataRequests = nil
	app.catalogLifecycleMu.Unlock()
	app.requestSessionCatalogMetadataSync()
	if len(current) != 0 {
		t.Fatal("stopped catalog received new metadata work")
	}
}
