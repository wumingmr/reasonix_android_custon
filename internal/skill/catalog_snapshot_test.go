package skill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestCatalogSnapshotColdCallersShareOneScanAndWarmReadsDoNotScan(t *testing.T) {
	root := t.TempDir()
	for i := range 1154 {
		path := filepath.Join(root, fmt.Sprintf("skill-%04d", i), SkillFile)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, fmt.Appendf(nil, "---\ndescription: skill %d\n---\nbody", i), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store := New(Options{HomeDir: t.TempDir(), CustomPaths: []string{root}, DisableBuiltins: true})
	t.Cleanup(func() { _ = store.Close() })

	const callers = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			snapshot, err := store.Snapshot(context.Background())
			if err != nil || len(snapshot.Candidates) != 1154 || !snapshot.Complete {
				t.Errorf("snapshot: count=%d complete=%v err=%v", len(snapshot.Candidates), snapshot.Complete, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if scans := store.DiscoveryScans(); scans != 1 {
		t.Fatalf("cold shared scans = %d, want 1", scans)
	}
	for i := range 2000 {
		if _, ok := store.Read(fmt.Sprintf("skill-%04d", i%1154)); !ok {
			t.Fatalf("warm indexed read %d failed", i)
		}
	}
	if scans := store.DiscoveryScans(); scans != 1 {
		t.Fatalf("warm reads rescanned: %d", scans)
	}
}

func TestCatalogSnapshotInvalidationPublishesOnlyCompleteReplacement(t *testing.T) {
	root := t.TempDir()
	write := func(name string) {
		path := filepath.Join(root, name, SkillFile)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("---\ndescription: test\n---\nbody"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("one")
	store := New(Options{HomeDir: t.TempDir(), CustomPaths: []string{root}, DisableBuiltins: true})
	t.Cleanup(func() { _ = store.Close() })
	first, err := store.Snapshot(context.Background())
	if err != nil || len(first.Candidates) != 1 {
		t.Fatalf("first snapshot = %+v, %v", first, err)
	}
	write("two")
	store.Invalidate("test")
	second, err := store.Snapshot(context.Background())
	if err != nil || len(second.Candidates) != 2 || second.Version == first.Version {
		t.Fatalf("second snapshot = %+v, %v", second, err)
	}
}

func TestLoadHonorsCancelledTurnBeforeDiscovery(t *testing.T) {
	store := New(Options{HomeDir: t.TempDir(), CustomPaths: []string{t.TempDir()}, DisableBuiltins: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := store.Load(ctx, "cancelled"); ok {
		t.Fatal("cancelled load resolved a skill")
	}
	if scans := store.DiscoveryScans(); scans != 0 {
		t.Fatalf("cancelled load started %d scans, want zero", scans)
	}
}

func TestWatchDirectoryScanHonorsCancellationBeforeFilesystemWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if directories, complete := watchDirectoriesContext(ctx, t.TempDir(), 3); complete || directories != nil {
		t.Fatalf("cancelled watch scan = (%v, %v), want (nil, false)", directories, complete)
	}
}

func TestCatalogWatcherInvalidatesCreateRenameAndDelete(t *testing.T) {
	root := t.TempDir()
	store := New(Options{HomeDir: t.TempDir(), CustomPaths: []string{root}, DisableBuiltins: true, Watch: true})
	t.Cleanup(func() { _ = store.Close() })
	first, err := store.Snapshot(context.Background())
	if err != nil || len(first.Candidates) != 0 {
		t.Fatalf("initial snapshot = %+v, %v", first, err)
	}

	write := func(name string) string {
		file := filepath.Join(root, name, SkillFile)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("---\ndescription: watched\n---\nbody"), 0o644); err != nil {
			t.Fatal(err)
		}
		return file
	}
	write("one")
	waitCatalogCount(t, store, 1)
	if err := os.Rename(filepath.Join(root, "one"), filepath.Join(root, "two")); err != nil {
		t.Fatal(err)
	}
	waitCatalogName(t, store, "two")
	if err := os.RemoveAll(filepath.Join(root, "two")); err != nil {
		t.Fatal(err)
	}
	waitCatalogCount(t, store, 0)
}

func waitCatalogCount(t *testing.T, store *Store, want int) CatalogSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := store.Snapshot(context.Background())
		if err == nil && len(snapshot.Candidates) == want {
			return snapshot
		}
		runtime.Gosched()
	}
	t.Fatalf("catalog did not reach count %d", want)
	return CatalogSnapshot{}
}

func waitCatalogName(t *testing.T, store *Store, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := store.Snapshot(context.Background())
		if err == nil && len(snapshot.Candidates) == 1 && snapshot.Candidates[0].Name == want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("catalog did not resolve %q", want)
}
