package skillwatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	frames := []frame{
		{Kind: wireRegister, ID: 7, RootGen: 3, Dirs: []string{"/a", "/b"}},
		{Kind: wireCancel, ID: 9},
		{Kind: wireEvent, ID: 7, RootGen: 3, Op: OpWrite},
		{Kind: wireError, ID: 7, Msg: "boom"},
	}
	for _, want := range frames {
		var buf bytes.Buffer
		if err := writeFrame(&buf, want); err != nil {
			t.Fatalf("writeFrame: %v", err)
		}
		got, err := readFrame(&buf)
		if err != nil {
			t.Fatalf("readFrame: %v", err)
		}
		if got.Kind != want.Kind || got.ID != want.ID || got.RootGen != want.RootGen || got.Op != want.Op || got.Msg != want.Msg || len(got.Dirs) != len(want.Dirs) {
			t.Fatalf("round trip mismatch: got %+v want %+v", got, want)
		}
	}
}

func TestReadFrameRejectsBadSize(t *testing.T) {
	if _, err := readFrame(bytes.NewReader([]byte{0, 0, 0, 0})); err == nil {
		t.Fatal("zero-size frame accepted")
	}
	if _, err := readFrame(bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff})); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

// countingScope reports the root itself plus one level of child directories,
// skipping the discovery-skipped bodies like the real scope does.
func countingScope(ctx context.Context, root string, maxDepth int) ([]string, bool) {
	dirs := []string{root}
	entries, err := os.ReadDir(root)
	if err != nil {
		return dirs, true
	}
	for _, e := range entries {
		if !e.IsDir() || maxDepth < 2 {
			continue
		}
		switch e.Name() {
		case "assets", "node_modules", "references", "scripts":
			continue
		}
		dirs = append(dirs, filepath.Join(root, e.Name()))
	}
	return dirs, true
}

func flatHash(ctx context.Context, root string, maxDepth int) ([sha256.Size]byte, int, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return [sha256.Size]byte{}, 0, false
	}
	h := sha256.New()
	count := 0
	for _, e := range entries {
		count++
		info, err := e.Info()
		if err != nil {
			continue
		}
		_, _ = h.Write([]byte(e.Name()))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(info.ModTime().String()))
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum, count, true
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSharedSubscriptionsCoalesceEvents(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(Options{Stderr: io.Discard})
	defer svc.Close()

	var mu sync.Mutex
	hits := map[string]int{}
	sub1 := svc.Subscribe(dir, 2, countingScope, flatHash, func(string) { mu.Lock(); hits["one"]++; mu.Unlock() })
	sub2 := svc.Subscribe(dir, 2, countingScope, flatHash, func(string) { mu.Lock(); hits["two"]++; mu.Unlock() })

	// Helper registration is bounded rather than unconditionally blocking, so
	// keep this assertion tolerant of either backend.
	waitFor(t, "physical watch registration", func() bool {
		return svc.Diagnostics().PhysicalWatches == 1
	})
	diag := svc.Diagnostics()
	if diag.LogicalSubscriptions != 2 {
		t.Fatalf("logical subscriptions = %d, want 2", diag.LogicalSubscriptions)
	}
	if diag.PhysicalWatches != 1 {
		t.Fatalf("physical watches = %d, want 1 (shared root)", diag.PhysicalWatches)
	}

	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "coalesced notifications", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return hits["one"] >= 1 && hits["two"] >= 1
	})

	// Both subscriptions share one root: one physical watch, one coalesced
	// window, two notifications.
	mu.Lock()
	first, second := hits["one"], hits["two"]
	mu.Unlock()
	if first != 1 || second != 1 {
		t.Fatalf("notifications one=%d two=%d, want 1/1 (coalesced)", first, second)
	}
	diag = svc.Diagnostics()
	if diag.EventsReceived < 1 || diag.Notifications != 1 {
		t.Fatalf("events=%d notifications=%d, want >=1/1", diag.EventsReceived, diag.Notifications)
	}

	sub1.Release()
	sub1.Release() // idempotent
	diag = svc.Diagnostics()
	if diag.LogicalSubscriptions != 1 {
		t.Fatalf("after release subscriptions = %d, want 1", diag.LogicalSubscriptions)
	}
	sub2.Release()
	waitFor(t, "physical watch teardown", func() bool {
		d := svc.Diagnostics()
		return d.LogicalSubscriptions == 0 && d.PhysicalWatches == 0
	})
	if diag := svc.Diagnostics(); diag.LogicalSubscriptions != 0 || diag.PhysicalWatches != 0 {
		t.Fatalf("after final release subscriptions=%d watches=%d, want 0/0", diag.LogicalSubscriptions, diag.PhysicalWatches)
	}
}

func TestScopeSkippedBodyChangesDoNotNotify(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Options{Stderr: io.Discard})
	defer svc.Close()

	var hits int
	var mu sync.Mutex
	svc.Subscribe(dir, 3, countingScope, flatHash, func(string) { mu.Lock(); hits++; mu.Unlock() })
	// Helper registration is bounded rather than unconditionally blocking, so
	// the count settles shortly after Subscribe returns on that backend.
	waitFor(t, "physical watch registration", func() bool {
		return svc.Diagnostics().PhysicalWatches == 1
	})

	if err := os.WriteFile(filepath.Join(dir, "scripts", "tool.sh"), []byte("echo hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("scripts/ change notified %d times, want 0", hits)
	}
}

func TestScanFallbackNotifiesOnHashDiff(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(Options{Stderr: io.Discard, ForceHelper: true, HelperCommand: func(ctx context.Context) (helperProcess, error) {
		return nil, errHelperStopped // helper never comes up
	}})
	defer svc.Close()

	var hits int
	var mu sync.Mutex
	svc.Subscribe(dir, 2, countingScope, flatHash, func(string) { mu.Lock(); hits++; mu.Unlock() })

	// First pass runs after the shortest backoff and establishes the baseline.
	waitFor(t, "degraded root diagnostics", func() bool {
		return svc.Diagnostics().DegradedRoots == 1
	})
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "scan fallback notification", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return hits >= 1
	})
	diag := svc.Diagnostics()
	if diag.Scans == 0 {
		t.Fatal("scan fallback produced no scans")
	}
}
