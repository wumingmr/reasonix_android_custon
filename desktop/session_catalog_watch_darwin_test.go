//go:build darwin && cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
	"reasonix/internal/sessioncatalog"
)

func TestCatalogWatchLargeRootUsesBoundedDescriptors(t *testing.T) {
	const child = "REASONIX_CATALOG_NOFILE_CHILD"
	if os.Getenv(child) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCatalogWatchLargeRootUsesBoundedDescriptors$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), child+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("catalog watcher exceeded descriptor budget: %v\n%s", err, output)
		}
		return
	}
	root := t.TempDir()
	for i := range 512 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%04d.jsonl", i)), []byte("unread body"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var old unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &old); err != nil {
		t.Fatal(err)
	}
	limit := old
	limit.Cur = min(256, old.Max)
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Setrlimit(unix.RLIMIT_NOFILE, &old) })
	watcher, err := newWorkspaceWatcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watcher.Close() })
	if _, ok := watcher.(*darwinWorkspaceWatcher); !ok {
		t.Fatalf("catalog backend enumerates source files: %T", watcher)
	}
	watched, dirty := map[string]bool{}, map[string]bool{}
	targets := []sessioncatalog.DirectoryTarget{{Path: root, Scope: "global"}}
	current := refreshCatalogWatchTargets(watcher, nil, targets, watched, dirty)
	key := canonicalWorkspaceRoot(root)
	if !watched[key] || !dirty[key] {
		t.Fatalf("initial watch/discovery missing: watched=%v dirty=%v", watched, dirty)
	}
	if count := darwinOpenFDCount(t); count >= 128 {
		t.Fatalf("catalog watch opened %d descriptors for 512 sessions", count)
	}
	clear(dirty)
	refreshCatalogWatchTargets(watcher, current, targets, watched, dirty)
	if len(dirty) != 0 {
		t.Fatalf("idle root scheduled another scan: %v", dirty)
	}
}
