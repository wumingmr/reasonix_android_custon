package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/browser"
)

// openTab opens one tab and returns it with a fresh snapshot's token.
func openTab(t *testing.T, exec *Executor, ctx context.Context) (browser.Tab, browser.Snapshot) {
	t.Helper()
	tab, err := exec.Open(ctx, browser.OpenRequest{OperationID: "op-open", URL: "https://example.test/"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	snap, err := exec.Snapshot(ctx, browser.SnapshotRequest{TabID: tab.ID})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.DocumentToken == "" {
		t.Fatal("snapshot returned no documentToken")
	}
	return tab, snap
}

func clickRequest(tab browser.Tab, token, op, ref string) browser.ActRequest {
	return browser.ActRequest{OperationID: op, TabID: tab.ID, DocumentToken: token, Action: browser.ActionClick, Ref: ref}
}

func TestReusedOperationIDIsRefusedAndNeverDispatched(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutor(t, f)
	ctx := context.Background()
	tab, snap := openTab(t, exec, ctx)

	if _, err := exec.Act(ctx, clickRequest(tab, snap.DocumentToken, "op-click", "e1")); err != nil {
		t.Fatalf("first click: %v", err)
	}
	dispatched := f.countCalls("Input.dispatchMouseEvent")
	if dispatched == 0 {
		t.Fatal("first click dispatched no mouse events")
	}
	_, err := exec.Act(ctx, clickRequest(tab, snap.DocumentToken, "op-click", "e1"))
	if err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("replayed operationId: got %v, want an already-used refusal", err)
	}
	if got := f.countCalls("Input.dispatchMouseEvent"); got != dispatched {
		t.Fatalf("replayed operationId dispatched %d more events", got-dispatched)
	}
}

func TestWriteAgainstAnotherDocumentTokenIsStale(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutor(t, f)
	ctx := context.Background()
	tab, _ := openTab(t, exec, ctx)

	_, err := exec.Act(ctx, clickRequest(tab, "d-somethingelse", "op-click", "e1"))
	if !errors.Is(err, browser.ErrStaleReference) {
		t.Fatalf("foreign token: got %v, want ErrStaleReference", err)
	}
	if f.countCalls("Input.dispatchMouseEvent") != 0 {
		t.Fatal("a stale write reached the page")
	}
}

func TestNavigationRetiresTheDocumentToken(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutor(t, f)
	ctx := context.Background()
	tab, snap := openTab(t, exec, ctx)

	if _, err := exec.Navigate(ctx, browser.NavigateRequest{OperationID: "op-nav", TabID: tab.ID, Action: browser.NavigateURL, URL: "https://example.test/next"}); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	_, err := exec.Act(ctx, clickRequest(tab, snap.DocumentToken, "op-click", "e1"))
	if !errors.Is(err, browser.ErrStaleReference) {
		t.Fatalf("write after navigation: got %v, want ErrStaleReference", err)
	}
}

func TestTakeOverBlocksWritesUntilTheNextSnapshot(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutor(t, f)
	ctx := context.Background()
	tab, snap := openTab(t, exec, ctx)

	f.takeOver()
	_, err := exec.Act(ctx, clickRequest(tab, snap.DocumentToken, "op-click", "e1"))
	if !errors.Is(err, browser.ErrTakenOver) {
		t.Fatalf("write after take-over: got %v, want ErrTakenOver", err)
	}
	if f.countCalls("Input.dispatchMouseEvent") != 0 {
		t.Fatal("a write reached a page the user had taken over")
	}
	// The take-over is sticky: only re-reading the page clears it.
	if _, err := exec.Act(ctx, clickRequest(tab, snap.DocumentToken, "op-click-2", "e1")); !errors.Is(err, browser.ErrTakenOver) {
		t.Fatalf("second write after take-over: got %v, want ErrTakenOver", err)
	}
	fresh, err := exec.Snapshot(ctx, browser.SnapshotRequest{TabID: tab.ID})
	if err != nil {
		t.Fatalf("snapshot after take-over: %v", err)
	}
	if fresh.DocumentToken == snap.DocumentToken {
		t.Fatal("snapshot after take-over reused the retired token")
	}
	if _, err := exec.Act(ctx, clickRequest(tab, fresh.DocumentToken, "op-click-3", "e1")); err != nil {
		t.Fatalf("write after re-reading the page: %v", err)
	}
}

func TestTabsAreInvisibleToAnotherSession(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutor(t, f)
	owner := browser.WithSession(context.Background(), "session-a")
	other := browser.WithSession(context.Background(), "session-b")
	tab, snap := openTab(t, exec, owner)

	tabs, err := exec.Tabs(other)
	if err != nil {
		t.Fatalf("tabs: %v", err)
	}
	if len(tabs) != 0 {
		t.Fatalf("another session sees %d tab(s)", len(tabs))
	}
	if _, err := exec.Act(other, clickRequest(tab, snap.DocumentToken, "op-click", "e1")); !errors.Is(err, browser.ErrNoGrant) {
		t.Fatalf("cross-session write: got %v, want ErrNoGrant", err)
	}
	tabs, err = exec.Tabs(owner)
	if err != nil || len(tabs) != 1 {
		t.Fatalf("owner tabs: %v %v", tabs, err)
	}
}

func TestFailureAfterTheFirstEventIsUnknown(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutor(t, f)
	ctx := context.Background()
	tab, snap := openTab(t, exec, ctx)

	seen := 0
	f.setHandler("Input.dispatchMouseEvent", func(json.RawMessage) (any, *protocolError) {
		seen++
		if seen > 1 {
			return nil, &protocolError{Code: -32000, Message: "target closed"}
		}
		return map[string]any{}, nil
	})
	res, err := exec.Act(ctx, clickRequest(tab, snap.DocumentToken, "op-click", "e1"))
	if !errors.Is(err, browser.ErrUnknownOutcome) {
		t.Fatalf("failure after the first event: got %v, want ErrUnknownOutcome", err)
	}
	if res.Outcome != browser.OutcomeUnknown {
		t.Fatalf("outcome = %q, want %q", res.Outcome, browser.OutcomeUnknown)
	}
}

func TestRefusalOnTheFirstEventIsNotExecuted(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutor(t, f)
	ctx := context.Background()
	tab, snap := openTab(t, exec, ctx)

	f.setHandler("Input.dispatchMouseEvent", func(json.RawMessage) (any, *protocolError) {
		return nil, &protocolError{Code: -32000, Message: "Input events are disabled"}
	})
	res, err := exec.Act(ctx, clickRequest(tab, snap.DocumentToken, "op-click", "e1"))
	if err != nil {
		t.Fatalf("refused click returned an error: %v", err)
	}
	if res.Executed || res.Outcome != browser.OutcomeNotExecuted {
		t.Fatalf("outcome = %+v, want a not_executed refusal", res)
	}
	if !strings.Contains(res.Reason, "Input events are disabled") {
		t.Fatalf("reason = %q, want the browser's refusal", res.Reason)
	}
}

func TestRefThatLeftTheDocumentIsStale(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutor(t, f)
	ctx := context.Background()
	tab, snap := openTab(t, exec, ctx)

	_, err := exec.Act(ctx, clickRequest(tab, snap.DocumentToken, "op-click", "e404"))
	if !errors.Is(err, browser.ErrStaleReference) {
		t.Fatalf("unknown ref: got %v, want ErrStaleReference", err)
	}
}

func TestTypeSendsOneKeyPairPerRuneAndSubmits(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutor(t, f)
	ctx := context.Background()
	tab, snap := openTab(t, exec, ctx)

	req := browser.ActRequest{
		OperationID: "op-type", TabID: tab.ID, DocumentToken: snap.DocumentToken,
		Action: browser.ActionType, Ref: "e1", Text: "hi", Submit: true,
	}
	if _, err := exec.Act(ctx, req); err != nil {
		t.Fatalf("type: %v", err)
	}
	// Two runes and one Enter, each a key-down and a key-up.
	if got := f.countCalls("Input.dispatchKeyEvent"); got != 6 {
		t.Fatalf("dispatched %d key events, want 6", got)
	}
	var last struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(f.lastArgs("Input.dispatchKeyEvent"), &last); err != nil {
		t.Fatalf("decode last key event: %v", err)
	}
	if last.Key != "Enter" {
		t.Fatalf("last key = %q, want Enter", last.Key)
	}
}

func TestScreenshotWritesATaskOwnedFile(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutor(t, f)
	ctx := context.Background()
	tab, _ := openTab(t, exec, ctx)

	shot, err := exec.Screenshot(ctx, browser.ScreenshotRequest{TabID: tab.ID})
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if shot.Width != 4 || shot.Height != 3 {
		t.Fatalf("screenshot is %dx%d, want 4x3", shot.Width, shot.Height)
	}
	if filepath.Dir(shot.Path) != filepath.Join(exec.artifacts, "screenshots") {
		t.Fatalf("screenshot landed at %s, outside the task's artifact directory", shot.Path)
	}
	if _, err := os.Stat(shot.Path); err != nil {
		t.Fatalf("screenshot file: %v", err)
	}
}

func TestDownloadsAreNamedAndListedPerTab(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutor(t, f)
	ctx := context.Background()
	tab, _ := openTab(t, exec, ctx)

	dir := filepath.Join(exec.artifacts, "downloads")
	if err := os.WriteFile(filepath.Join(dir, "guid-1"), []byte("id,name\n"), 0o600); err != nil {
		t.Fatalf("stage download: %v", err)
	}
	f.emit("", "Browser.downloadWillBegin", map[string]any{
		"frameId": "frame-1", "guid": "guid-1", "url": "https://example.test/report.csv", "suggestedFilename": "report.csv",
	})
	f.emit("", "Browser.downloadProgress", map[string]any{
		"guid": "guid-1", "state": "completed", "receivedBytes": 8, "totalBytes": 8,
	})
	// One more round trip on the same socket: its reply cannot arrive before
	// the events above were read and dispatched.
	if _, err := exec.Tabs(ctx); err != nil {
		t.Fatalf("tabs: %v", err)
	}
	list, err := exec.Downloads(ctx, browser.DownloadsRequest{TabID: tab.ID})
	if err != nil {
		t.Fatalf("downloads: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("listed %d downloads, want 1", len(list))
	}
	if got := filepath.Base(list[0].Path); got != "report.csv" {
		t.Fatalf("download saved as %q, want report.csv", got)
	}
	if list[0].State != "completed" || list[0].Bytes != 8 {
		t.Fatalf("download = %+v, want a completed 8-byte entry", list[0])
	}
}

func TestClosedExecutorFailsClosed(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutor(t, f)
	ctx := context.Background()
	tab, snap := openTab(t, exec, ctx)

	exec.Shutdown()
	if exec.Available(ctx) {
		t.Fatal("a closed executor still reports itself available")
	}
	if _, err := exec.Act(ctx, clickRequest(tab, snap.DocumentToken, "op-click", "e1")); !errors.Is(err, browser.ErrNoGrant) {
		t.Fatalf("write after shutdown: got %v, want ErrNoGrant", err)
	}
}
