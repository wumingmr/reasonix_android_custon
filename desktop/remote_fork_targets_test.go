package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// forkTestCapabilities is what a current Serve advertises; the fork token is
// appended only for the serve that actually mounts the fork routes.
const forkTestCapabilities = "execution-v2,session-history-v1,session-identity-v1,session-ownership-v1"

const forkTestSessionID = "parent-1"

// forkServeCall is one request the fork serve received. It keeps the fence
// header the bridge attached, which is what proves a command was fenced to the
// session the tab had open.
type forkServeCall struct {
	method, path, body, fence string
}

// forkServe is a Serve stand-in for the remote fork bindings: the token
// handshake with a caller-chosen capability list, the endpoints a remote tab
// needs to reach ready, and the two fork routes under test. Every request but
// the handshake and the event stream is recorded, so a binding that reached a
// route it should not have is visible to the assertions.
type forkServe struct {
	t                *testing.T
	token            string
	caps             string
	server           *httptest.Server
	mu               sync.Mutex
	calls            []forkServeCall
	targets          string
	forkBody         string
	forkStatus       int
	dropForkResponse bool
}

func newForkServe(t *testing.T, forkCapable bool) *forkServe {
	t.Helper()
	fs := &forkServe{
		t: t, token: "s3cret", caps: forkTestCapabilities,
		targets:  `{"source":{"hostId":"box","sessionId":"parent-1"},"targets":[],"verifiable":false}`,
		forkBody: `{"sessionId":"child-1","turnId":"turn-1","turnNumber":1}`,
	}
	if forkCapable {
		fs.caps += "," + serveCapabilitySessionForkTargetsV1
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/token", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token != fs.token {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "reasonix_token", Value: fs.token, Path: "/", HttpOnly: true})
		w.Header().Set(serveCapabilitiesHeader, fs.caps)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /new", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Reasonix-Session-ID", forkTestSessionID)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, []serveSessionEntry{
			{Name: "current", Current: true, HostID: "box", SessionID: forkTestSessionID},
		})
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"state":"ready"}`))
	})
	mux.HandleFunc("GET /history", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		for _, frame := range []string{`{"kind":"session_start"}`, `{"kind":"ready"}`} {
			fmt.Fprintf(w, "data: %s\n\n", frame)
		}
		flusher.Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("GET /fork-targets", func(w http.ResponseWriter, _ *http.Request) {
		fs.mu.Lock()
		payload := fs.targets
		fs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	})
	mux.HandleFunc("POST /fork-session", func(w http.ResponseWriter, _ *http.Request) {
		fs.mu.Lock()
		status, payload := fs.forkStatus, fs.forkBody
		drop := fs.dropForkResponse
		fs.dropForkResponse = false
		fs.mu.Unlock()
		if drop {
			if hijacker, ok := w.(http.Hijacker); ok {
				connection, _, _ := hijacker.Hijack()
				_ = connection.Close()
				return
			}
		}
		if status != 0 {
			http.Error(w, payload, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	})
	gate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/token" && r.URL.Path != "/events" {
			fs.record(r)
		}
		if r.URL.Path == "/auth/token" {
			mux.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie("reasonix_token"); err == nil && c.Value == fs.token {
			mux.ServeHTTP(w, r)
			return
		}
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
	fs.server = httptest.NewServer(gate)
	t.Cleanup(fs.server.Close)
	return fs
}

// record keeps one request. Bodies are re-marshaled from their decoded object
// so the recorded form does not depend on the field order the binding wrote.
func (fs *forkServe) record(r *http.Request) {
	call := forkServeCall{
		method: r.Method, path: r.URL.Path,
		fence: r.Header.Get(expectedSessionIDHeader),
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<10)); err == nil && len(raw) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var decoded any
		if json.Unmarshal(raw, &decoded) == nil {
			normalized, _ := json.Marshal(decoded)
			call.body = string(normalized)
		}
	}
	fs.mu.Lock()
	fs.calls = append(fs.calls, call)
	fs.mu.Unlock()
}

func forkRemoteAnchor(a *App, tabID, turnID string, boundary uint64) ForkAnchorView {
	a.remoteTabMu.Lock()
	defer a.remoteTabMu.Unlock()
	return ForkAnchorView{SourceHostID: "box", SourceSessionID: forkTestSessionID,
		SessionGeneration: a.remoteTabs[tabID].gen, TurnID: turnID, BoundarySequence: boundary}
}

func (fs *forkServe) recorded() []forkServeCall {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]forkServeCall(nil), fs.calls...)
}

// openForkTab attaches a remote tab to this serve's session and waits for it,
// which is when the handshake capabilities are recorded on the tab.
func openForkTab(t *testing.T, fs *forkServe) (*App, TabMeta) {
	t.Helper()
	isolateDesktopUserDirs(t)
	kernel := &fakeRemoteKernel{
		statuses:    []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView:  RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL},
		ensureToken: fs.token,
	}
	seedBridgeTestHost(t, "box")
	a := &App{remoteRuntime: kernel}
	cleanupRemoteTabPumps(t, a)
	return a, openReadyRemoteTab(t, a, RemoteTabOpenOptions{NewSession: true})
}

// TestForkTargetsCapableRejectsUnknownAndUnadvertisedTabs pins the default: an
// unknown tab, a tab whose handshake has not run, and a serve that advertised
// no fork capability all report false, so no older serve is treated as capable.
func TestForkTargetsCapableRejectsUnknownAndUnadvertisedTabs(t *testing.T) {
	fs := newForkServe(t, false)
	a, meta := openForkTab(t, fs)

	if a.forkTargetsCapable("missing") {
		t.Fatal("unknown tab reported fork-targets capable")
	}
	a.remoteTabMu.Lock()
	a.remoteTabs["pending"] = &remoteTab{id: "pending"}
	a.remoteTabMu.Unlock()
	if a.forkTargetsCapable("pending") {
		t.Fatal("tab without a handshake reported fork-targets capable")
	}
	if a.forkTargetsCapable(meta.ID) {
		t.Fatalf("serve advertising %q reported fork-targets capable", fs.caps)
	}

	capable := newForkServe(t, true)
	capableApp, capableMeta := openForkTab(t, capable)
	if !capableApp.forkTargetsCapable(capableMeta.ID) {
		t.Fatalf("serve advertising %q reported fork-targets incapable", capable.caps)
	}
}

// assertTabMetaForkTargetsSupported reads one tab exactly as the renderer does,
// through ListTabs, and requires the field to be emitted even when it is false.
func assertTabMetaForkTargetsSupported(t *testing.T, a *App, tabID string, want bool) {
	t.Helper()
	found := false
	for _, meta := range a.ListTabs() {
		if meta.ID != tabID {
			continue
		}
		found = true
		if meta.ForkTargetsSupported != want {
			t.Fatalf("tab %q forkTargetsSupported = %v, want %v", tabID, meta.ForkTargetsSupported, want)
		}
		raw, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("tab %q: marshal meta: %v", tabID, err)
		}
		if !strings.Contains(string(raw), `"forkTargetsSupported":`) {
			t.Fatalf("tab %q meta = %s, want forkTargetsSupported always emitted", tabID, raw)
		}
	}
	if !found {
		t.Fatalf("tab %q is missing from ListTabs", tabID)
	}
}

// TestRemoteTabMetaForkTargetsSupportedFollowsHandshake pins the renderer's
// view: the tab a capable serve backs reads true, the tab behind a serve that
// never advertised the capability reads false, and a tab whose handshake has
// not run yet reads false too.
func TestRemoteTabMetaForkTargetsSupportedFollowsHandshake(t *testing.T) {
	capable, capableMeta := openForkTab(t, newForkServe(t, true))
	unsupported, unsupportedMeta := openForkTab(t, newForkServe(t, false))

	assertTabMetaForkTargetsSupported(t, capable, capableMeta.ID, true)
	assertTabMetaForkTargetsSupported(t, unsupported, unsupportedMeta.ID, false)

	// A restored shell reaches the strip before its handshake, so its meta must
	// report false rather than leave the field out.
	unsupported.remoteTabMu.Lock()
	unsupported.remoteTabs["no-handshake"] = &remoteTab{
		id: "no-handshake", state: "connecting",
		ref: RemoteTabRef{HostID: "box", Workspace: "~/app"},
	}
	unsupported.remoteTabMu.Unlock()
	assertTabMetaForkTargetsSupported(t, unsupported, "no-handshake", false)
}

// TestLocalTabMetaForkTargetsSupportedIsFalse pins that a local tab never reads
// as supported: the local tab builder does not set the field, and the field has
// no omitempty, so a renderer sees false rather than a missing field.
func TestLocalTabMetaForkTargetsSupportedIsFalse(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	tab, err := app.EnsureBlankTab("global", "")
	if err != nil {
		t.Fatal(err)
	}
	assertTabMetaForkTargetsSupported(t, app, tab.ID, false)
}

// TestForkTargetsRemoteTabEmptyWithoutCapability pins the empty answer: no
// error the caller must string-match, a slice that marshals as [] rather than
// null, and no request to a route the serve does not have.
func TestForkTargetsRemoteTabEmptyWithoutCapability(t *testing.T) {
	fs := newForkServe(t, false)
	a, meta := openForkTab(t, fs)
	before := len(fs.recorded())

	for _, tabID := range []string{meta.ID, "missing"} {
		view, err := a.ForkTargetsRemoteTab(tabID)
		if err != nil {
			t.Fatalf("ForkTargetsRemoteTab(%q): %v", tabID, err)
		}
		if view.Targets == nil {
			t.Fatalf("ForkTargetsRemoteTab(%q).Targets is nil; frontend expects []", tabID)
		}
		encoded, err := json.Marshal(view)
		if err != nil {
			t.Fatalf("marshal view: %v", err)
		}
		if !strings.Contains(string(encoded), `"targets":[]`) {
			t.Fatalf("ForkTargetsRemoteTab(%q) encoded %s, want an empty array", tabID, encoded)
		}
	}
	if got := fs.recorded()[before:]; len(got) != 0 {
		t.Fatalf("incapable serve received %+v, want no request at all", got)
	}
}

// TestForkTargetsRemoteTabDecodesServeTargets pins the read path: the serve's
// targets and verifiability reach the view unchanged.
func TestForkTargetsRemoteTabDecodesServeTargets(t *testing.T) {
	fs := newForkServe(t, true)
	fs.targets = `{"source":{"hostId":"box","sessionId":"parent-1"},"targets":[{"turnId":"turn-1","boundarySequence":7,"turnNumber":1,"status":"committed","messageId":"m1","available":true},` +
		`{"turnId":"turn-2","turnNumber":2,"status":"open","available":false,"reason":"turn_open"}],"verifiable":true}`
	a, meta := openForkTab(t, fs)

	view, err := a.ForkTargetsRemoteTab(meta.ID)
	if err != nil {
		t.Fatalf("ForkTargetsRemoteTab: %v", err)
	}
	if !view.Verifiable || len(view.Targets) != 2 {
		t.Fatalf("view = %+v, want two verifiable targets", view)
	}
	if view.Targets[0].TurnID != "turn-1" || !view.Targets[0].Available || view.Targets[0].MessageID != "m1" {
		t.Fatalf("first target = %+v", view.Targets[0])
	}
	if view.Targets[1].Available || view.Targets[1].Reason != "turn_open" {
		t.Fatalf("second target = %+v, want the open-turn refusal", view.Targets[1])
	}
	fenced := false
	for _, call := range fs.recorded() {
		if call.path == "/fork-targets" && call.fence == forkTestSessionID {
			fenced = true
		}
	}
	if !fenced {
		t.Fatal("fork target GET did not carry the expected session id")
	}
}

func TestCreateForkRemoteTabRejectsStaleSourceBeforePosting(t *testing.T) {
	fs := newForkServe(t, true)
	a, meta := openForkTab(t, fs)
	anchor := forkRemoteAnchor(a, meta.ID, "shared-turn", 9)
	a.remoteTabMu.Lock()
	a.remoteTabs[meta.ID].routing.currentPath = remoteSessionIDRoutePrefix + "source-b"
	a.remoteTabMu.Unlock()
	before := len(fs.recorded())
	view, err := a.CreateForkRemoteTab(meta.ID, anchor)
	if err != nil || view.Reason != "stale_source" {
		t.Fatalf("stale remote create = %+v, err=%v", view, err)
	}
	if got := fs.recorded()[before:]; len(got) != 0 {
		t.Fatalf("stale remote create reached Serve: %+v", got)
	}
}

func TestCreateForkRemoteTabReusesOperationAfterLostResponse(t *testing.T) {
	fs := newForkServe(t, true)
	a, meta := openForkTab(t, fs)
	anchor := forkRemoteAnchor(a, meta.ID, "turn-1", 9)
	fs.mu.Lock()
	fs.dropForkResponse = true
	fs.mu.Unlock()
	before := len(fs.recorded())
	if _, err := a.CreateForkRemoteTab(meta.ID, anchor); err == nil {
		t.Fatal("dropped response unexpectedly succeeded")
	}
	// Reconstruct Desktop and its remote tab before retrying. The journal is a
	// host file, so neither the App instance nor the restored tab id is part of
	// the idempotency key.
	restarted := &App{remoteRuntime: &fakeRemoteKernel{
		statuses:    []RemoteConnectionStatusView{{HostID: "box", State: "connected"}},
		ensureView:  RemoteServerView{HostID: "box", State: "ready", LocalURL: fs.server.URL},
		ensureToken: fs.token,
	}}
	cleanupRemoteTabPumps(t, restarted)
	restartedMeta := openReadyRemoteTab(t, restarted, RemoteTabOpenOptions{NewSession: true})
	restartedAnchor := forkRemoteAnchor(restarted, restartedMeta.ID, anchor.TurnID, anchor.BoundarySequence)
	view, err := restarted.CreateForkRemoteTab(restartedMeta.ID, restartedAnchor)
	if err != nil || view.SessionID != "child-1" || view.OperationID == "" {
		t.Fatalf("retry = %+v, err=%v", view, err)
	}
	var operations []string
	for _, call := range fs.recorded()[before:] {
		if call.path != "/fork-session" {
			continue
		}
		var body map[string]any
		if json.Unmarshal([]byte(call.body), &body) == nil {
			operations = append(operations, fmt.Sprint(body["operationId"]))
		}
	}
	if len(operations) != 2 || operations[0] == "" || operations[0] != operations[1] {
		t.Fatalf("operation ids across unknown-result retry = %v", operations)
	}
}

// TestCreateForkRemoteTabRefusesUnsupportedServe pins that a serve without the
// capability gets the unsupported reason and no fork request at all: falling
// back to /fork would switch the parent session the caller must keep.
func TestCreateForkRemoteTabRefusesUnsupportedServe(t *testing.T) {
	fs := newForkServe(t, false)
	a, meta := openForkTab(t, fs)
	before := len(fs.recorded())

	view, err := a.CreateForkRemoteTab(meta.ID, forkRemoteAnchor(a, meta.ID, "turn-1", 7))
	if err != nil {
		t.Fatalf("CreateForkRemoteTab: %v", err)
	}
	if view.Opened {
		t.Fatal("unsupported serve reported an opened fork")
	}
	if !strings.Contains(view.Error, serveCapabilitySessionForkTargetsV1) {
		t.Fatalf("unsupported error = %q, want the capability named", view.Error)
	}
	if got := fs.recorded()[before:]; len(got) != 0 {
		t.Fatalf("unsupported serve received %+v, want no request at all", got)
	}
}

// TestCreateForkRemoteTabRequiresTurnID pins that a missing turn id is a
// programming error rather than a state a serve could refuse.
func TestCreateForkRemoteTabRequiresTurnID(t *testing.T) {
	a := &App{}
	if _, err := a.CreateForkRemoteTab("missing", ForkAnchorView{TurnID: "  "}); err == nil {
		t.Fatal("empty turn id was accepted")
	}
}

// TestCreateForkRemoteTabPostsFencedForkSession pins the create path: one
// fenced POST to /fork-session carrying the turn and operation, a child
// identity in the view, and no request that would rebind the parent session.
func TestCreateForkRemoteTabPostsFencedForkSession(t *testing.T) {
	fs := newForkServe(t, true)
	a, meta := openForkTab(t, fs)
	before := len(fs.recorded())

	view, err := a.CreateForkRemoteTab(meta.ID, forkRemoteAnchor(a, meta.ID, "turn-4", 9))
	if err != nil {
		t.Fatalf("CreateForkRemoteTab: %v", err)
	}
	if !view.Opened || view.SessionID != "child-1" || view.Error != "" {
		t.Fatalf("view = %+v, want the opened child", view)
	}
	posted := false
	for _, call := range fs.recorded()[before:] {
		// /new, /resume, /clear, and /fork all move or reload the session the
		// tab has open; creating a child must leave that route and lease alone.
		switch call.path {
		case "/fork-session":
			posted = true
			if call.method != http.MethodPost {
				t.Fatalf("fork-session method = %s", call.method)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(call.body), &body); err != nil {
				t.Fatalf("fork-session body %s: %v", call.body, err)
			}
			if body["turnId"] != "turn-4" || body["operationId"] == "" || body["sourceSessionId"] != forkTestSessionID || body["boundarySequence"] != float64(9) {
				t.Fatalf("fork-session body = %s, want the turn and operation", call.body)
			}
			if call.fence != forkTestSessionID {
				t.Fatalf("fork-session fence = %q, want the parent session id %q", call.fence, forkTestSessionID)
			}
		case "/fork", "/new", "/resume", "/clear":
			t.Fatalf("create fork reached the session-switching route %s", call.path)
		}
	}
	if !posted {
		t.Fatalf("no fork-session request: %+v", fs.recorded()[before:])
	}
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[meta.ID]
	path, state := tab.routing.currentPath, tab.state
	a.remoteTabMu.Unlock()
	if path != remoteSessionIDRoutePrefix+forkTestSessionID || state != "ready" {
		t.Fatalf("parent tab moved to path %q state %q, want its original session still ready", path, state)
	}
}

// TestCreateForkRemoteTabSurfacesRefusalReason pins that a refusal status keeps
// the serve's own words instead of a status code.
func TestCreateForkRemoteTabSurfacesRefusalReason(t *testing.T) {
	fs := newForkServe(t, true)
	a, meta := openForkTab(t, fs)
	const reason = `session: turn "turn-2" cannot start a fork (turn_open)`
	fs.mu.Lock()
	fs.forkStatus, fs.forkBody = http.StatusConflict, `{"code":"fork_unavailable","reason":"turn_open","message":"`+strings.ReplaceAll(reason, `"`, `\"`)+`"}`
	fs.mu.Unlock()

	view, err := a.CreateForkRemoteTab(meta.ID, forkRemoteAnchor(a, meta.ID, "turn-2", 9))
	if err != nil {
		t.Fatalf("CreateForkRemoteTab: %v", err)
	}
	if view.Opened || view.Error != reason {
		t.Fatalf("view = %+v, want the refusal %q with nothing opened", view, reason)
	}
	journal, loadErr := loadForkOperations(forkOperationsPath())
	if loadErr != nil || len(journal.Operations) != 0 {
		t.Fatalf("explicit remote refusal journal = %+v, err=%v", journal, loadErr)
	}
}
