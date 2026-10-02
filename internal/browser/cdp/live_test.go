package cdp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"reasonix/internal/browser"
)

// livePage exercises the snapshot walker and every input path the executor
// dispatches: a labelled text box, a submit button that reports what it got,
// a select, and a link the walker must name.
const livePage = `<!doctype html>
<html><body>
<h1>Live fixture</h1>
<form id="f" onsubmit="event.preventDefault(); document.getElementById('out').textContent = 'submitted:' + q.value + ':' + pick.value;">
  <label for="q">Query</label>
  <input id="q" name="q" type="text" placeholder="Search">
  <select id="pick" name="pick"><option value="one">One</option><option value="two">Two</option></select>
  <button type="submit">Run search</button>
</form>
<input id="secret" type="password" value="hunter2">
<a href="https://example.test/docs">Docs</a>
<div id="out"></div>
<div id="hidden" style="display:none"><button>Never</button></div>
</body></html>`

// TestLiveChrome drives a real browser. It stays skipped in normal CI because
// it needs a Chrome install; it is the only test that runs the injected
// isolated-world helper against a real DOM.
//
// Run with:
//
//	REASONIX_LIVE_CHROME=1 go test ./internal/browser/cdp \
//	  -run '^TestLiveChrome$' -v -count=1 -timeout=3m
func TestLiveChrome(t *testing.T) {
	if os.Getenv("REASONIX_LIVE_CHROME") != "1" {
		t.Skip("set REASONIX_LIVE_CHROME=1 to run the real Chrome end-to-end test")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(livePage))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	exec, err := New(ctx, Options{Headless: true, ArtifactDir: t.TempDir(), NavigateTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("launch chrome: %v", err)
	}
	defer exec.Shutdown()

	tab, err := exec.Open(ctx, browser.OpenRequest{OperationID: "live-open", URL: srv.URL})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	snap, err := exec.Snapshot(ctx, browser.SnapshotRequest{TabID: tab.ID})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	t.Logf("snapshot refs=%d\n%s", snap.Refs, snap.Tree)
	for _, want := range []string{`textbox "Query"`, `button "Run search"`, `link "Docs"`, "combobox"} {
		if !strings.Contains(snap.Tree, want) {
			t.Errorf("snapshot tree is missing %s:\n%s", want, snap.Tree)
		}
	}
	if strings.Contains(snap.Tree, "hunter2") {
		t.Errorf("the snapshot leaked a password field's value:\n%s", snap.Tree)
	}
	if strings.Contains(snap.Tree, `"Never"`) {
		t.Errorf("the snapshot walked a display:none subtree:\n%s", snap.Tree)
	}

	queryRef := refFor(t, snap.Tree, `textbox "Query"`)
	pickRef := refFor(t, snap.Tree, "combobox")
	buttonRef := refFor(t, snap.Tree, `button "Run search"`)

	act(t, ctx, exec, browser.ActRequest{
		OperationID: "live-type", TabID: tab.ID, DocumentToken: snap.DocumentToken,
		Action: browser.ActionType, Ref: queryRef, Text: "reasonix",
	})
	act(t, ctx, exec, browser.ActRequest{
		OperationID: "live-select", TabID: tab.ID, DocumentToken: snap.DocumentToken,
		Action: browser.ActionSelect, Ref: pickRef, Options: []string{"Two"},
	})
	act(t, ctx, exec, browser.ActRequest{
		OperationID: "live-click", TabID: tab.ID, DocumentToken: snap.DocumentToken,
		Action: browser.ActionClick, Ref: buttonRef,
	})

	after, err := exec.Snapshot(ctx, browser.SnapshotRequest{TabID: tab.ID, Selector: "#out"})
	if err != nil {
		t.Fatalf("snapshot after acting: %v", err)
	}
	if !strings.Contains(after.Tree, "submitted:reasonix:two") {
		t.Fatalf("the page did not observe the typed text, the selection, and the click:\n%s", after.Tree)
	}
	if after.DocumentToken == snap.DocumentToken {
		t.Fatal("a second snapshot reused the first token")
	}
	// The retired token must not be usable even though the document never left.
	if _, err := exec.Act(ctx, browser.ActRequest{
		OperationID: "live-stale", TabID: tab.ID, DocumentToken: snap.DocumentToken,
		Action: browser.ActionClick, Ref: buttonRef,
	}); err == nil {
		t.Fatal("a write against the retired token was accepted")
	}

	shot, err := exec.Screenshot(ctx, browser.ScreenshotRequest{TabID: tab.ID, FullPage: true})
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if shot.Width == 0 || shot.Height == 0 {
		t.Fatalf("screenshot has no size: %+v", shot)
	}
	if err := exec.Close(ctx, browser.CloseRequest{OperationID: "live-close", TabID: tab.ID}); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func act(t *testing.T, ctx context.Context, exec *Executor, req browser.ActRequest) {
	t.Helper()
	res, err := exec.Act(ctx, req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Action, req.Ref, err)
	}
	if !res.Executed {
		t.Fatalf("%s %s was not executed: %s", req.Action, req.Ref, res.Reason)
	}
}

// refFor returns the ref of the first snapshot line containing want.
func refFor(t *testing.T, tree, want string) string {
	t.Helper()
	for line := range strings.SplitSeq(tree, "\n") {
		if !strings.Contains(line, want) {
			continue
		}
		_, rest, ok := strings.Cut(line, "[ref=")
		if !ok {
			continue
		}
		if ref, _, ok := strings.Cut(rest, "]"); ok {
			return ref
		}
	}
	t.Fatalf("no ref for %q in:\n%s", want, tree)
	return ""
}
