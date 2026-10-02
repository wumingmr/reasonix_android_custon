package fileops

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMutationLockSurvivesReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	unlock := Lock(DiskTarget(path, info))
	if err := os.Rename(path, path+".old"); err != nil {
		unlock()
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new"), 0o600); err != nil {
		unlock()
		t.Fatal(err)
	}
	info, _ = os.Stat(path)
	started, acquired := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		release := Lock(DiskTarget(path, info))
		release()
		close(acquired)
	}()
	<-started
	select {
	case <-acquired:
		unlock()
		t.Fatal("replacement bypassed the in-flight mutation lock")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement lock did not release")
	}
}

func TestLiveObservationCloneIsIndependent(t *testing.T) {
	first := NewStore()
	target := OverlayTarget("file")
	first.ObservePresent(target, "v1")
	second := first.Clone()
	first.Forget(target)
	if got := second.Get(target); got.Kind != Present || got.Version != "v1" {
		t.Fatalf("live transfer lost observation: %+v", got)
	}
	second.ObservePresent(target, "v2")
	if got := first.Get(target); got.Kind != Unseen {
		t.Fatalf("replacement runtime mutated old runtime: %+v", got)
	}
}

type linuxStatInfo struct {
	os.FileInfo
	meta any
}

func (s linuxStatInfo) Sys() any { return s.meta }

func TestDiskVersionIncludesLinuxCtim(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Linux syscall.Stat_t uses Ctim, while Darwin uses Ctimespec. Keep this
	// contract runnable on every host, with identical size, mode and mtime.
	type timespec struct{ Sec, Nsec int64 }
	type stat struct{ Ctim timespec }
	before := linuxStatInfo{info, stat{timespec{1, 10}}}
	after := linuxStatInfo{info, stat{timespec{1, 11}}}
	if DiskVersion(before) == DiskVersion(after) {
		t.Fatal("Linux Ctim nanosecond change was omitted from the version")
	}
}
