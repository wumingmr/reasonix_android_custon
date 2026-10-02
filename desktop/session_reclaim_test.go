package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestOwnershipProbeFailurePreservesSpectatorPin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary failure", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	app := NewApp()
	app.remoteTabs = map[string]*remoteTab{}
	tab := &remoteTab{
		id: "remote-1", gen: 4, client: srv.Client(), selectionRevision: 9,
		routing: remoteTabSessionRouting{currentPath: "/sessions/a.jsonl"},
		session: remoteTabSessionState{takenOver: true},
	}
	app.remoteTabs[tab.id] = tab
	app.markRemoteTabSpectatorIfLocalOwned(context.Background(), tab.id, tab.client, srv.URL, tab.gen)
	if !tab.session.takenOver {
		t.Fatal("failed ownership probe cleared the spectator pin")
	}
}

func TestLateOwnershipProbeCannotChangeNewSelection(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_ = json.NewEncoder(w).Encode(SessionTakeoverView{Holder: "external", Mirrored: true})
	}))
	defer srv.Close()
	app := NewApp()
	app.remoteTabs = map[string]*remoteTab{}
	tab := &remoteTab{
		id: "remote-1", gen: 4, client: srv.Client(), selectionRevision: 9,
		routing: remoteTabSessionRouting{currentPath: "/sessions/old.jsonl"},
	}
	app.remoteTabs[tab.id] = tab
	done := make(chan struct{})
	go func() {
		app.markRemoteTabSpectatorIfLocalOwned(context.Background(), tab.id, tab.client, srv.URL, tab.gen)
		close(done)
	}()
	<-started
	app.remoteTabMu.Lock()
	tab.selectionRevision++
	tab.routing.currentPath = "/sessions/new.jsonl"
	app.remoteTabMu.Unlock()
	close(release)
	<-done
	if tab.session.takenOver {
		t.Fatal("late ownership probe marked the newer selection read-only")
	}
}

func TestLateReclaimSuccessCannotUnlockNewSelection(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/reclaim" {
			close(started)
			<-release
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "not available", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	app := NewApp()
	app.remoteTabs = map[string]*remoteTab{}
	tab := &remoteTab{
		id: "remote-1", state: "ready", gen: 4, client: srv.Client(), base: srv.URL, selectionRevision: 9,
		routing:      remoteTabSessionRouting{currentPath: "/sessions/old.jsonl"},
		session:      remoteTabSessionState{takenOver: true},
		capabilities: map[string]bool{serveCapabilityExecutionV2: true, serveCapabilitySessions: true, serveCapabilitySessionIdentityV1: true, serveCapabilitySessionOwnershipV1: true},
	}
	app.remoteTabs[tab.id] = tab
	done := make(chan error, 1)
	go func() { done <- app.ReclaimRemoteTabSession(tab.id) }()
	<-started
	app.remoteTabMu.Lock()
	tab.selectionRevision++
	tab.routing.currentPath = "/sessions/new.jsonl"
	tab.session.takenOver = true
	app.remoteTabMu.Unlock()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !tab.session.takenOver {
		t.Fatal("late reclaim response unlocked the newer selection")
	}
}

func TestFailedReclaimKeepsSpectatorUntilOwnershipProbeCompletes(t *testing.T) {
	probeStarted := make(chan struct{})
	probeRelease := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/reclaim":
			http.Error(w, "mirror generation changed", http.StatusConflict)
		case "/ownership":
			close(probeStarted)
			<-probeRelease
			_ = json.NewEncoder(w).Encode(SessionTakeoverView{Holder: "external", Mirrored: true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	app := NewApp()
	app.remoteTabs = map[string]*remoteTab{}
	tab := &remoteTab{
		id: "remote-1", state: "ready", gen: 4, client: srv.Client(), base: srv.URL, selectionRevision: 9,
		routing:      remoteTabSessionRouting{currentPath: "/sessions/a.jsonl"},
		session:      remoteTabSessionState{takenOver: true},
		capabilities: map[string]bool{serveCapabilityExecutionV2: true, serveCapabilitySessions: true, serveCapabilitySessionIdentityV1: true, serveCapabilitySessionOwnershipV1: true},
	}
	app.remoteTabs[tab.id] = tab
	if err := app.ReclaimRemoteTabSession(tab.id); err == nil {
		t.Fatal("failed reclaim unexpectedly succeeded")
	}
	<-probeStarted
	if !tab.session.takenOver {
		t.Fatal("ambiguous reclaim failure unlocked input before ownership proof")
	}
	close(probeRelease)
	app.remoteTabTasks.Wait()
	if !tab.session.takenOver {
		t.Fatal("external owner probe cleared spectator state")
	}
}

func TestSuccessfulReclaimRepublishesReadyBarrier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/reclaim" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "not available", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	app := NewApp()
	app.remoteTabs = map[string]*remoteTab{}
	tab := &remoteTab{
		id: "remote-1", state: "ready", gen: 4, client: srv.Client(), base: srv.URL, selectionRevision: 9,
		routing:      remoteTabSessionRouting{currentPath: "/sessions/held.jsonl"},
		session:      remoteTabSessionState{takenOver: true},
		capabilities: map[string]bool{serveCapabilityExecutionV2: true, serveCapabilitySessions: true, serveCapabilitySessionIdentityV1: true, serveCapabilitySessionOwnershipV1: true},
	}
	app.remoteTabs[tab.id] = tab

	var mu sync.Mutex
	var events []string
	app.remoteEventHook = func(name string, _ any) {
		mu.Lock()
		events = append(events, name)
		mu.Unlock()
	}
	if err := app.ReclaimRemoteTabSession(tab.id); err != nil {
		t.Fatal(err)
	}
	if tab.session.takenOver {
		t.Fatal("successful reclaim left the spectator pin in place")
	}
	mu.Lock()
	defer mu.Unlock()
	sawReady := false
	for _, name := range events {
		if name == "remote-tab:remote-1:state" {
			sawReady = true
		}
	}
	if !sawReady {
		t.Fatalf("reclaim did not republish the ready barrier: %v", events)
	}
}

func TestReclaimBarrierDefersWhileTurnInFlight(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/reclaim":
			w.WriteHeader(http.StatusNoContent)
			return
		case "/status":
			// The post-reclaim refresh observes the surface idle and free.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sessionPath":"/sessions/held.jsonl","sessionId":"held","running":false,"takenOver":false}`))
			return
		}
		http.Error(w, "not available", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	app := NewApp()
	app.remoteTabs = map[string]*remoteTab{}
	tab := &remoteTab{
		id: "remote-1", state: "ready", gen: 4, client: srv.Client(), base: srv.URL, selectionRevision: 9,
		routing:      remoteTabSessionRouting{currentPath: "/sessions/held.jsonl"},
		session:      remoteTabSessionState{takenOver: true},
		runtime:      remoteTabRuntimeState{running: true},
		capabilities: map[string]bool{serveCapabilityExecutionV2: true, serveCapabilitySessions: true, serveCapabilitySessionIdentityV1: true, serveCapabilitySessionOwnershipV1: true},
	}
	app.remoteTabs[tab.id] = tab

	var mu sync.Mutex
	var events []string
	app.remoteEventHook = func(name string, _ any) {
		mu.Lock()
		events = append(events, name)
		mu.Unlock()
	}
	if err := app.ReclaimRemoteTabSession(tab.id); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	for _, name := range events {
		if name == "remote-tab:remote-1:state" {
			mu.Unlock()
			t.Fatal("reclaim published the ready barrier while a turn was in flight")
		}
	}
	mu.Unlock()
	if !tab.ownership.readyBarrierPending {
		t.Fatal("reclaim did not defer the barrier for the running turn")
	}

	// The deferred barrier fires once polling observes the surface idle. The
	// reclaim's own asynchronous status refresh drives that poll against the
	// serve, so the test only waits for the ready publication.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		sawReady := false
		for _, name := range events {
			if name == "remote-tab:remote-1:state" {
				sawReady = true
			}
		}
		mu.Unlock()
		pending := func() bool {
			app.remoteTabMu.Lock()
			defer app.remoteTabMu.Unlock()
			return tab.ownership.readyBarrierPending
		}()
		if sawReady && !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("deferred barrier did not fire on idle: ready=%v pending=%v", sawReady, pending)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// An in-flight /status response reserved before an explicit reclaim can land
// after ownership returned, still mirroring the pre-reclaim takenOver=true.
// The reclaim epoch must keep such a payload from re-pinning the banner.
func TestStaleStatusCannotRepinTakenOverAfterReclaim(t *testing.T) {
	a := &App{remoteTabs: map[string]*remoteTab{}}
	client := &http.Client{}
	tab := &remoteTab{
		id: "remote-1", state: "ready", gen: 2, client: client,
		routing: remoteTabSessionRouting{currentPath: "session-id:held"},
		session: remoteTabSessionState{takenOver: false},
		// The reclaim stamped epoch 5; the in-flight poll reserved revision 3.
		runtime: remoteTabRuntimeState{revision: 3},
	}
	tab.ownership.reclaimRevision = 5
	a.remoteTabs[tab.id] = tab
	payload := []byte(`{"sessionId":"held","takenOver":true}`)

	if !a.recordRemoteTabSessionStatus(tab.id, client, 2, 3, payload) {
		t.Fatal("fenced recorder rejected the payload entirely")
	}
	if tab.session.takenOver {
		t.Fatal("pre-reclaim status payload re-pinned the spectator banner")
	}

	// A post-reclaim observation (reserved after the epoch) still applies in
	// both directions — including a genuine re-takeover by the local runtime.
	tab.runtime.revision = 5
	if !a.recordRemoteTabSessionStatus(tab.id, client, 2, 5, payload) {
		t.Fatal("post-reclaim status payload was rejected")
	}
	if !tab.session.takenOver {
		t.Fatal("post-reclaim ownership observation did not apply")
	}
	tab.runtime.revision = 6
	tab.ownership.reclaimRevision = 6
	released := []byte(`{"sessionId":"held","takenOver":false}`)
	if !a.recordRemoteTabSessionStatus(tab.id, client, 2, 6, released) {
		t.Fatal("fresh release observation was rejected")
	}
	if tab.session.takenOver {
		t.Fatal("release observation did not clear the pin")
	}
}
