package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// identityOwnershipServe answers /ownership for an identity-routed session,
// records the selector each probe asked about, and runs duringProbe while the
// answer is still in flight.
func identityOwnershipServe(t *testing.T, holder string, asked *atomic.Value, duringProbe func()) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ownership" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		asked.Store(r.URL.Query().Get("session"))
		if duringProbe != nil {
			duringProbe()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":true,"holder":"` + holder + `","sessionPath":"session-id:held"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func identityNoticeTab(srv *httptest.Server) (*App, *remoteTab) {
	tab := &remoteTab{
		id: "remote-1", state: "ready", gen: 4, client: srv.Client(), base: srv.URL,
		routing: remoteTabSessionRouting{currentPath: "session-id:held", running: map[string]bool{}},
		session: remoteTabSessionState{sessionID: "held"},
	}
	return &App{remoteTabs: map[string]*remoteTab{tab.id: tab}}, tab
}

// Serve now delivers takeover and reclaim notices on identity routes, which it
// previously dropped for this subscriber. The tab must route such a notice to
// the ownership probe under its identity route and adopt the answer.
func TestIdentityRoutedTakeoverNoticeProbesAndPinsSpectator(t *testing.T) {
	var asked atomic.Value
	srv := identityOwnershipServe(t, "external", &asked, nil)
	a, tab := identityNoticeTab(srv)

	a.probeSpectatorAfterNotice(tab.id, tab.gen, tab.client, tab.base, "session-id:held")

	if got, _ := asked.Load().(string); got != "session-id:held" {
		t.Fatalf("ownership probe asked about %q, want the identity route", got)
	}
	a.remoteTabMu.Lock()
	takenOver := tab.session.takenOver
	a.remoteTabMu.Unlock()
	if !takenOver {
		t.Fatal("an identity-routed takeover notice did not pin the spectator state")
	}
}

// A reclaimed notice for a session that is no longer locally owned clears the
// pin, so ownership returning through a notice does not leave a stale banner.
func TestIdentityRoutedReclaimedNoticeClearsSpectator(t *testing.T) {
	var asked atomic.Value
	srv := identityOwnershipServe(t, "free", &asked, nil)
	a, tab := identityNoticeTab(srv)
	tab.session.takenOver = true

	a.probeSpectatorAfterNotice(tab.id, tab.gen, tab.client, tab.base, "session-id:held")

	a.remoteTabMu.Lock()
	takenOver := tab.session.takenOver
	a.remoteTabMu.Unlock()
	if takenOver {
		t.Fatal("a reclaimed notice left the spectator banner pinned")
	}
}

// A notice for a different session must not probe or re-pin this tab.
func TestIdentityRoutedNoticeForAnotherSessionIsIgnored(t *testing.T) {
	var asked atomic.Value
	srv := identityOwnershipServe(t, "external", &asked, nil)
	a, tab := identityNoticeTab(srv)

	a.probeSpectatorAfterNotice(tab.id, tab.gen, tab.client, tab.base, "session-id:other")

	if asked.Load() != nil {
		t.Fatal("a notice for another session probed ownership for this tab")
	}
	a.remoteTabMu.Lock()
	takenOver := tab.session.takenOver
	a.remoteTabMu.Unlock()
	if takenOver {
		t.Fatal("a notice for another session pinned the spectator banner")
	}
}

// Now that these notices arrive on identity routes, one can land around an
// explicit take-back. A probe whose answer predates the reclaim must not
// re-pin the banner after ownership returned — the fence the reclaim epoch
// already applies to in-flight status payloads.
func TestOwnershipProbeCannotRepinAcrossACompletedReclaim(t *testing.T) {
	var asked atomic.Value
	var a *App
	var tab *remoteTab
	// The reclaim completes while the stale "still externally held" answer is
	// in flight, so the ordering needs no timing assumptions.
	srv := identityOwnershipServe(t, "external", &asked, func() {
		a.remoteTabMu.Lock()
		tab.session.takenOver = false
		tab.ownership.reclaimRevision = tab.runtime.revision + 1
		a.remoteTabMu.Unlock()
	})
	a, tab = identityNoticeTab(srv)
	tab.session.takenOver = true

	a.probeSpectatorAfterNotice(tab.id, tab.gen, tab.client, tab.base, "session-id:held")

	a.remoteTabMu.Lock()
	takenOver := tab.session.takenOver
	a.remoteTabMu.Unlock()
	if takenOver {
		t.Fatal("a pre-reclaim ownership probe re-pinned the spectator banner")
	}
}
