package fileops

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStoreTransitions(t *testing.T) {
	s := NewStore()
	target := OverlayTarget("a.txt")
	if got := s.Get(target); got.Kind != Unseen {
		t.Fatalf("initial kind = %v", got.Kind)
	}
	s.ObserveAbsent(target)
	if got := s.Get(target); got.Kind != Absent {
		t.Fatalf("absent kind = %v", got.Kind)
	}
	s.ObservePresent(target, "v1")
	if got := s.Get(target); got.Kind != Present || got.Version != "v1" {
		t.Fatalf("present = %#v", got)
	}
	s.Forget(target)
	if got := s.Get(target); got.Kind != Unseen {
		t.Fatalf("forgotten kind = %v", got.Kind)
	}
}

func TestDiskVersionDetectsMetadataChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows permission freshness is covered with a native ACL change")
	}
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if DiskVersion(before) == DiskVersion(after) {
		t.Fatal("permission change did not change version")
	}
}

func TestHardLinksShareTarget(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, b); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	ai, _ := os.Stat(a)
	bi, _ := os.Stat(b)
	if DiskTarget(a, ai).Key != DiskTarget(b, bi).Key {
		t.Fatalf("targets differ: %#v %#v", DiskTarget(a, ai), DiskTarget(b, bi))
	}
}

func TestOverlayProviderIdentitiesDoNotShareObservations(t *testing.T) {
	store := NewStore()
	first := OverlayTargetWithIdentity("buffer.txt", "connection-a")
	second := OverlayTargetWithIdentity("buffer.txt", "connection-b")
	store.ObservePresent(first, OverlayVersion("same content"))
	if got := store.Get(second); got.Kind != Unseen {
		t.Fatalf("observation crossed overlay providers: %+v", got)
	}
}
