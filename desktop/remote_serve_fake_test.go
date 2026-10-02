package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"reasonix/internal/config"
)

// fakeServe is a minimal Serve stand-in for bridge tests: token handshake,
// session enter, an SSE feed that emits two frames then holds, and recorded
// command endpoints for the proxy bindings.
type fakeServe struct {
	t      *testing.T
	token  string
	server *httptest.Server

	mu                             sync.Mutex
	newCalled                      int
	newSessionPath                 string
	resumePath                     string
	resumeSessionID                string
	cookieOnNew                    bool
	sessions                       []serveSessionEntry
	calls                          []string // "METHOD /path body" per command request
	expectedPaths                  []string // foreground command fence headers
	failNext                       string   // non-empty ⇒ next command endpoint replies 409 with this text
	failEnter                      string   // non-empty ⇒ next /new or /resume replies 409
	enterDelay                     time.Duration
	newStarted, newRelease         chan struct{}
	resumeStarted                  chan string
	resumeRelease                  chan struct{}
	failHistory                    bool // /history replies 500 when set
	historyBody                    string
	historyStarted, historyRelease chan struct{}
	failSessions                   bool // /sessions replies 500 when set
	sessionsFailCount              int  // /sessions replies 500 this many times, then recovers
	resumeDropCount                int  // /resume commits the switch but drops the connection unanswered this many times
	sessionsStarted                chan struct{}
	sessionsRelease                chan struct{}
	eventsConns                    int // /events connections opened
	eventsQuery                    string
	eventFrames                    []string
	eventFeed                      <-chan string
	eventsStatus                   int  // non-zero makes /events fail before opening
	eventsFailCount                int  // refuse this many /events opens with 503, then serve normally
	eventsCloseEarly               bool // return immediately after the initial 200 frames
	statusPayload                  string
	statusAfterCancel              string
	contextExpected                string
	contextStarted, contextRelease chan struct{}
}

func (fs *fakeServe) eventsCount() int { fs.mu.Lock(); defer fs.mu.Unlock(); return fs.eventsConns }

func (fs *fakeServe) recorded() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make([]string, len(fs.calls))
	copy(out, fs.calls)
	return out
}

func (fs *fakeServe) recordedExpectedPaths() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.expectedPaths...)
}

func (fs *fakeServe) record(method, path, body string) {
	fs.mu.Lock()
	fs.calls = append(fs.calls, method+" "+path+" "+body)
	fs.mu.Unlock()
}

// newFakeServe builds a stand-in for the workspace Serve's HTTP surface. The
// mux is wrapped in a token-mode gate that mirrors the real authGate's
// contract: POST /auth/token is matched on the EXACT path (a "//auth/token"
// double slash — what naive base+path joins produce from EnsureServer's
// trailing-slash LocalURL — is denied with 401 before routing), and every
// other path requires the session cookie the bootstrap installs. A bare mux
// cannot catch this: it 301-redirects unclean paths and Go's client follows
// preserving POST, so a double-slash request would silently succeed here
// while the real Serve rejects it.
func newFakeServe(t *testing.T, token string, sessions []serveSessionEntry) *fakeServe {
	t.Helper()
	fs := &fakeServe{t: t, token: token, sessions: sessions}
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
		w.Header().Set(serveCapabilitiesHeader, "permission-presets-v1,present-files-v1,execution-v2,session-history-v1,session-identity-v1,session-ownership-v1")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /new", func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		fs.newCalled++
		_, cookieErr := r.Cookie("reasonix_token")
		fs.cookieOnNew = cookieErr == nil
		fail := fs.failEnter
		fs.failEnter = ""
		enterDelay := fs.enterDelay
		newSessionPath := fs.newSessionPath
		newStarted, newRelease := fs.newStarted, fs.newRelease
		if fail != "" {
			fs.mu.Unlock()
			http.Error(w, fail, http.StatusConflict)
			return
		}
		// The serve abandons the current session on /new: no file, not listed.
		for i := range fs.sessions {
			fs.sessions[i].Current = false
		}
		fs.mu.Unlock()
		if newStarted != nil {
			select {
			case newStarted <- struct{}{}:
			default:
			}
		}
		if newRelease != nil {
			select {
			case <-newRelease:
			case <-r.Context().Done():
				return
			}
		}
		if enterDelay > 0 {
			time.Sleep(enterDelay)
		}
		if newSessionPath != "" {
			w.Header().Set("X-Reasonix-Session-Path", newSessionPath)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /resume", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path      string `json:"path"`
			SessionID string `json:"sessionId"`
		}
		// Mirrors the real handler: a canonical row carries only sessionId, so
		// requiring a path here would hide every identity-route regression.
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" && body.SessionID == "" {
			http.Error(w, "missing path or sessionId", http.StatusBadRequest)
			return
		}
		fs.mu.Lock()
		fail := fs.failEnter
		fs.failEnter = ""
		enterDelay := fs.enterDelay
		resumeStarted, resumeRelease := fs.resumeStarted, fs.resumeRelease
		drop := fs.takeFault(&fs.resumeDropCount)
		if fail != "" {
			fs.mu.Unlock()
			http.Error(w, fail, http.StatusConflict)
			return
		}
		fs.resumePath, fs.resumeSessionID = body.Path, body.SessionID
		for i := range fs.sessions {
			fs.sessions[i].Current = body.SessionID != "" && fs.sessions[i].SessionID == body.SessionID ||
				body.SessionID == "" && fs.sessions[i].Path == body.Path
		}
		fs.mu.Unlock()
		if body.SessionID != "" {
			w.Header().Set("X-Reasonix-Session-ID", body.SessionID)
		}
		if drop {
			// Serve committed the switch; only the response is lost.
			dropHTTPConnection(w)
			return
		}
		if resumeStarted != nil {
			select {
			case resumeStarted <- body.Path:
			default:
			}
		}
		if resumeRelease != nil {
			select {
			case <-resumeRelease:
			case <-r.Context().Done():
				return
			}
		}
		if enterDelay > 0 {
			time.Sleep(enterDelay)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		fs.record(r.Method, "/sessions", "")
		fs.mu.Lock()
		fail := fs.failSessions || fs.takeFault(&fs.sessionsFailCount)
		started, release := fs.sessionsStarted, fs.sessionsRelease
		sessions := append([]serveSessionEntry(nil), fs.sessions...)
		fs.mu.Unlock()
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		if release != nil {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if fail {
			http.Error(w, "sessions unavailable", http.StatusInternalServerError)
			return
		}
		writeTestJSON(w, sessions)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		fs.eventsConns++
		fs.eventsQuery = r.URL.RawQuery
		eventsStatus := fs.eventsStatus
		if fs.takeFault(&fs.eventsFailCount) && eventsStatus == 0 {
			eventsStatus = http.StatusServiceUnavailable
		}
		closeEarly := fs.eventsCloseEarly
		frames := append([]string(nil), fs.eventFrames...)
		feed := fs.eventFeed
		fs.mu.Unlock()
		if eventsStatus != 0 {
			http.Error(w, "event stream unavailable", eventsStatus)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		if len(frames) == 0 {
			frames = []string{`{"kind":"session_start"}`, `{"kind":"ready"}`}
		}
		for _, frame := range frames {
			fmt.Fprintf(w, "data: %s\n\n", frame)
		}
		flusher.Flush()
		if closeEarly {
			return
		}
		if feed == nil {
			<-r.Context().Done()
			return
		}
		for {
			select {
			case frame := <-feed:
				fmt.Fprintf(w, "data: %s\n\n", frame)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	command := func(path string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
			fs.record(r.Method, path, string(data))
			fs.mu.Lock()
			fs.expectedPaths = append(fs.expectedPaths, r.Header.Get(expectedSessionPathHeader))
			fail := fs.failNext
			fs.failNext = ""
			if path == "/cancel" && fs.statusAfterCancel != "" {
				fs.statusPayload = fs.statusAfterCancel
			}
			fs.mu.Unlock()
			if fail != "" {
				http.Error(w, fail, http.StatusConflict)
				return
			}
			if path == "/composer-profile" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"drainedApprovalIDs":["approval-1"]}`)
				return
			}
			if path == "/permission/preset" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"snapshot":{"sessionId":"remote-session","generation":1,"revision":8,"preset":"workspace-write","workspaceRoot":"/workspace","grants":[],"capabilities":{"backend":"seatbelt","enforcement":"full","supportedPresets":["read-only","workspace-write","danger-full-access"]}}}`)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}
	for _, path := range []string{"/submit", "/cancel", "/approve", "/plan-decision", "/answer", "/extension-form", "/rewind", "/goal", "/goal/edit", "/goal/pause", "/goal/resume", "/jobs/cancel", "/inbox/items", "/permission/preset", "/composer-profile", "/delete-session", "/model", "/effort", "/quality-floor", "/plan", "/compact", "/fork", "/summarize", "/forget", "/clear"} {
		mux.HandleFunc("POST "+path, command(path))
	}
	snapshot := func(path, payload string) {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			fs.record(r.Method, path, "")
			responsePayload := payload
			if path == "/context" {
				fs.mu.Lock()
				fs.contextExpected = r.Header.Get(expectedSessionPathHeader)
				if id := r.Header.Get(expectedSessionIDHeader); id != "" {
					fs.contextExpected = remoteSessionIDRoutePrefix + id
				}
				started, release := fs.contextStarted, fs.contextRelease
				fs.mu.Unlock()
				if started != nil {
					started <- struct{}{}
				}
				if release != nil {
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
			}
			if path == "/history" {
				fs.mu.Lock()
				fail := fs.failHistory
				if fs.historyBody != "" {
					responsePayload = fs.historyBody
				}
				started, release := fs.historyStarted, fs.historyRelease
				fs.mu.Unlock()
				if started != nil {
					started <- struct{}{}
				}
				if release != nil {
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				if fail {
					http.Error(w, "gone", http.StatusInternalServerError)
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(responsePayload))
		})
	}
	snapshot("/history", `[{"role":"user","content":"hi"}]`)
	snapshot("/context", `{"used":10,"window":128}`)
	snapshot("/todos", `[]`)
	snapshot("/checkpoints", `[{"turn":1}]`)
	snapshot("/models", `{"current":"remote/chat","label":"chat","models":[{"ref":"remote/chat","provider":"remote","model":"chat","active":true}]}`)
	snapshot("/commands", `[{"name":"remote-review","description":"Review remotely","kind":"custom","group":"skills"}]`)
	snapshot("/pending-prompts", `[{"kind":"approval_request","approval":{"id":"approval-1","tool":"bash"}}]`)
	snapshot("/permission", `{"sessionId":"remote-session","generation":1,"revision":7,"preset":"workspace-write","workspaceRoot":"/workspace","grants":[],"capabilities":{"backend":"seatbelt","enforcement":"full","supportedPresets":["read-only","workspace-write","danger-full-access"]}}`)
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		fs.record(r.Method, "/status", "")
		fs.mu.Lock()
		payload := fs.statusPayload
		fs.mu.Unlock()
		if payload == "" {
			payload = `{"state":"ready"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	})
	snapshot("/branches", `{"branches":[]}`)
	snapshot("/skills", `[]`)
	gate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func (fs *fakeServe) snapshot() (newCalled int, resumePath string, cookieOnNew bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.newCalled, fs.resumePath, fs.cookieOnNew
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestRemoteHostFixtureIsolatesCanonicalWorkspaceRegistry(t *testing.T) {
	isolateDesktopUserDirs(t)
	prior := NewApp()
	if _, err := prior.ensureDesktopWorkspace(t.Context(), "global", ""); err != nil {
		t.Fatal(err)
	}
	seedBridgeTestHost(t, "box")
	fresh := NewApp()
	if _, err := fresh.ensureDesktopWorkspace(t.Context(), "global", ""); err != nil {
		t.Fatalf("remote fixture reused another home's canonical registry: %v", err)
	}
}

func seedBridgeTestHost(t *testing.T, hostID string) {
	t.Helper()
	// A new config home also needs its own canonical registry: global workspace
	// identity contains that home, while REASONIX_STATE_HOME otherwise survives.
	home := isolateDesktopUserDirs(t)
	t.Setenv("REASONIX_HOME", home)
	if err := editUserConfig(func(c *config.Config) error {
		return c.UpsertRemoteHost(config.RemoteHostEntry{Name: hostID, Host: "127.0.0.1", Port: 22, User: "dev"})
	}); err != nil {
		t.Fatal(err)
	}
}

func waitForRemoteEventCount(t *testing.T, log *eventLog, prefix string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := log.count(prefix); got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("event count for %q = %d, want >= %d (events: %v)", prefix, log.count(prefix), want, log.recorded())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// cleanupRemoteTabPumps cancels every open tab's SSE pump and waits for all
// bridge tasks to return. Waiting matters on Windows: an async resume can
// publish its final tab snapshot after the assertion succeeds, racing the
// temporary user directory's cleanup.
func cleanupRemoteTabPumps(t *testing.T, a *App) {
	t.Helper()
	t.Cleanup(func() {
		a.remoteTabMu.Lock()
		for _, tab := range a.remoteTabs {
			if tab.cancel != nil {
				tab.cancel()
			}
		}
		a.remoteTabMu.Unlock()
		done := make(chan struct{})
		go func() { a.remoteTabTasks.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("remote tab tasks did not stop after pump cancellation")
		}
	})
}

// openReadyRemoteTab opens a tab against the fake serve and waits for ready.
func openReadyRemoteTab(t *testing.T, a *App, opts RemoteTabOpenOptions) TabMeta {
	t.Helper()
	meta, err := a.OpenRemoteProjectTab("box", "~/app", opts)
	if err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, a, meta.ID, "ready")
	return meta
}
