package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"reasonix/internal/servecontract"
	"reasonix/internal/session"
)

func TestRemoteHistoryOutlineCapabilityCutAndFailure(t *testing.T) {
	var reads atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		q := r.URL.Query()
		if r.URL.Path != "/session-history/outline" || q.Get("sessionId") != "canonical" || q.Get("generation") != "cut" || q.Get("snapshotSequence") != "9" || q.Get("startTurn") != "129" || q.Get("limit") != "128" {
			t.Errorf("unexpected outline request: %s", r.URL)
		}
		if fail.Load() {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(session.HistoryOutlinePage{Status: "ready", Generation: "cut", SnapshotSequence: 9, TotalTurns: 200})
	}))
	defer server.Close()
	app, tab := historyWindowFixture(server)
	cut := uint64(9)
	req := session.HistoryOutlineRequest{Generation: "cut", SnapshotSequence: &cut, StartTurn: 129, Limit: 128}
	page, err := app.RemoteSessionHistoryOutlineForTab(tab.id, req)
	if err != nil || page.Status != "unsupported" || reads.Load() != 0 {
		t.Fatalf("capability: %+v %v reads=%d", page, err, reads.Load())
	}
	tab.capabilities[servecontract.HistoryOutlineV1] = true
	page, err = app.RemoteSessionHistoryOutlineForTab(tab.id, req)
	if err != nil || page.Status != "ready" || page.Entries == nil {
		t.Fatalf("ready: %+v %v", page, err)
	}
	fail.Store(true)
	if _, err := app.RemoteSessionHistoryOutlineForTab(tab.id, req); err == nil {
		t.Fatal("network failure must remain retryable")
	}
	if reads.Load() != 2 {
		t.Fatalf("unexpected fallback: %d reads", reads.Load())
	}
}
