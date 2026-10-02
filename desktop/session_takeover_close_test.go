package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// closeMirrorFixture registers a mirror for sessionPath against a stub Serve
// that records when the farewell arrives.
func closeMirrorFixture(t *testing.T, app *App, sessionPath string) (*takeoverMirror, *atomic.Int32) {
	t.Helper()
	var ends atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mirror-end" {
			ends.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	m := &takeoverMirror{
		app: app, key: sessionRuntimeKey(sessionPath), sessionPath: sessionPath,
		client: srv.Client(), record: takeoverServeRecord{base: srv.URL},
		grant: takeoverGrant{MirrorID: "mirror"},
		wake:  make(chan struct{}, 1),
	}
	app.takeoverMu.Lock()
	if app.takeoverMirrors == nil {
		app.takeoverMirrors = map[string]*takeoverMirror{}
	}
	app.takeoverMirrors[m.key] = m
	app.takeoverMu.Unlock()
	return m, &ends
}

// Serve waits for the writer to drop before reclaiming a mirrored session, so
// the farewell must follow the writer release rather than race it. A closing
// tab claims the farewell while it still owns the writer and sends it after
// the release; the mirror loop's tab-gone branch must not pre-empt that.
func TestClosingTabOwnsTheMirrorFarewellUntilTheWriterIsReleased(t *testing.T) {
	app := NewApp()
	path := filepath.Join(t.TempDir(), "closing.jsonl")
	m, ends := closeMirrorFixture(t, app, path)

	claimed, release := app.claimTakeoverMirrorFarewell(path)
	if claimed != m || !m.closing.Load() {
		t.Fatalf("claim = %p, want the registered mirror marked closing", claimed)
	}
	defer release()
	// While the close is still tearing the writer down, the loop's tab-gone
	// branch only detaches: announcing the writer as gone here is exactly the
	// ordering that makes Serve wait.
	m.detach()
	if !m.closing.Load() {
		t.Fatal("detach cleared the close claim")
	}
	if ends.Load() != 0 {
		t.Fatalf("farewell sent %d times before the writer was released", ends.Load())
	}

	app.endTakeoverMirrorForClosedTab(m)
	deadline := time.Now().Add(20 * time.Second)
	for ends.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the close epilogue never sent the farewell")
		}
		time.Sleep(time.Millisecond)
	}

	// A delivered farewell is not repeated: the loop's backstop and the close
	// epilogue must not both announce the writer as gone.
	m.mirrorEnd()
	if got := ends.Load(); got != 1 {
		t.Fatalf("farewell sent %d times, want exactly one", got)
	}
}

// An undelivered farewell stays unmarked so a later path can retry it.
func TestUndeliveredMirrorFarewellStaysRetryable(t *testing.T) {
	app := NewApp()
	path := filepath.Join(t.TempDir(), "unreachable.jsonl")
	m, _ := closeMirrorFixture(t, app, path)
	m.mu.Lock()
	m.record = takeoverServeRecord{base: "http://127.0.0.1:1"}
	m.mu.Unlock()

	m.mirrorEnd()
	if m.ended.Load() {
		t.Fatal("a failed farewell was recorded as delivered")
	}
}

// A close that returns early keeps its writer, so the farewell claim goes back
// to the mirror loop instead of stranding the mirror silent.
func TestAbandonedCloseReturnsTheFarewellClaim(t *testing.T) {
	app := NewApp()
	path := filepath.Join(t.TempDir(), "abandoned.jsonl")
	m, ends := closeMirrorFixture(t, app, path)

	func() {
		_, release := app.claimTakeoverMirrorFarewell(path)
		defer release()
		// The close bails out here (detached runtime, or the tab changed).
	}()

	if m.closing.Load() {
		t.Fatal("an abandoned close kept the farewell claim")
	}
	m.mirrorEnd()
	if ends.Load() != 1 {
		t.Fatalf("farewell sent %d times after the claim was returned, want one", ends.Load())
	}
}
