package cdp

import (
	"context"
	"strings"
	"testing"
	"time"

	"reasonix/internal/browser"
)

func TestLazyAttachesOnFirstUseOnly(t *testing.T) {
	f := newFakeBrowser(t)
	lazy := NewLazy(Options{Endpoint: f.srv.URL, ArtifactDir: t.TempDir(), NavigateTimeout: 5 * time.Second})
	t.Cleanup(lazy.Shutdown)
	ctx := context.Background()

	if !lazy.Available(ctx) {
		t.Fatal("a configured but unattached browser must stay visible")
	}
	if f.countCalls("Browser.setDownloadBehavior") != 0 {
		t.Fatal("the browser was attached before any tool call")
	}
	if _, err := lazy.Tabs(ctx); err != nil {
		t.Fatalf("tabs: %v", err)
	}
	if f.countCalls("Browser.setDownloadBehavior") != 1 {
		t.Fatal("the first tool call did not attach the browser")
	}
	if _, err := lazy.Tabs(ctx); err != nil {
		t.Fatalf("second tabs: %v", err)
	}
	if got := f.countCalls("Browser.setDownloadBehavior"); got != 1 {
		t.Fatalf("attached %d times, want once", got)
	}
}

func TestLazyReportsAStartFailureWithoutRetrying(t *testing.T) {
	// A loopback port nothing listens on: the probe fails fast and for a
	// reason the model can act on.
	lazy := NewLazy(Options{Endpoint: "http://127.0.0.1:1", ArtifactDir: t.TempDir(), LaunchTimeout: 300 * time.Millisecond})
	t.Cleanup(lazy.Shutdown)
	ctx := context.Background()

	_, first := lazy.Tabs(ctx)
	if first == nil {
		t.Fatal("an unreachable endpoint attached anyway")
	}
	if !strings.Contains(first.Error(), "DevTools") {
		t.Fatalf("start failure = %v, want a DevTools endpoint diagnosis", first)
	}
	started := time.Now()
	_, second := lazy.Open(ctx, browser.OpenRequest{OperationID: "op-1", URL: "https://example.test/"})
	if second == nil || second.Error() != first.Error() {
		t.Fatalf("second call = %v, want the remembered start failure %v", second, first)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("the second call waited %s, so it retried the launch", elapsed)
	}
	if lazy.Available(ctx) {
		t.Fatal("a browser that cannot start still reports itself available")
	}
}
