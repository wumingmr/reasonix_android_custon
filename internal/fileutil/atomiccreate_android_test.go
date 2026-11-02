package fileutil

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func denyNewFileLinks(t *testing.T) {
	t.Helper()
	oldLink, oldRename := linkNewFile, renameNewFile
	linkNewFile = func(string, string) error { return syscall.EACCES }
	t.Cleanup(func() { linkNewFile, renameNewFile = oldLink, oldRename })
}

func TestAtomicCreateAndroidPublishesOnlyCompleteFile(t *testing.T) {
	denyNewFileLinks(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "record.json")
	data := bytes.Repeat([]byte("complete record\n"), 1024)
	var staged string
	renameNewFile = func(src, dst string) error {
		staged = src
		if _, err := os.Lstat(dst); !os.IsNotExist(err) {
			t.Fatalf("target visible before atomic publication: %v", err)
		}
		got, err := os.ReadFile(src)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("staged content incomplete: len=%d err=%v", len(got), err)
		}
		return RenameNoReplace(src, dst)
	}
	if err := AtomicCreateFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("published content incomplete: len=%d err=%v", len(got), err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("published mode changed: %v err=%v", info, err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged file leaked: %v", err)
	}
}

func TestAtomicCreateAndroidNoReplaceRace(t *testing.T) {
	denyNewFileLinks(t)
	path := filepath.Join(t.TempDir(), "key")
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() { errs <- AtomicCreateFile(path, []byte("complete"), 0o600) })
	}
	wg.Wait()
	close(errs)
	wins := 0
	for err := range errs {
		if err == nil {
			wins++
		} else if !errors.Is(err, os.ErrExist) {
			t.Fatalf("unexpected losing writer error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("successful writers = %d, want 1", wins)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "complete" {
		t.Fatalf("winner content = %q, %v", got, err)
	}
}

func TestAtomicCreateAndroidFailsClosedWithoutRename(t *testing.T) {
	denyNewFileLinks(t)
	renameNewFile = func(string, string) error { return syscall.ENOSYS }
	dir := t.TempDir()
	path := filepath.Join(dir, "receipt")
	if err := AtomicCreateFile(path, []byte("complete"), 0o600); !errors.Is(err, syscall.ENOSYS) {
		t.Fatalf("unsupported atomic publication: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed publication left files: %v, %v", entries, err)
	}
}

func TestAtomicCreateAndroidDirectorySyncAfterCommit(t *testing.T) {
	denyNewFileLinks(t)
	path := filepath.Join(t.TempDir(), "receipt")
	called := false
	restore := SetSyncParentDirForTest(func(target string) error {
		called = true
		got, err := os.ReadFile(target)
		if target != path || err != nil || string(got) != "complete" {
			t.Fatalf("directory sync before publication: %q %v", got, err)
		}
		return syscall.EIO
	})
	defer restore()
	if err := AtomicCreateFile(path, []byte("complete"), 0o600); err != nil {
		t.Fatalf("post-commit sync failure reported as failed write: %v", err)
	}
	if !called {
		t.Fatal("published directory was not synced")
	}
}
