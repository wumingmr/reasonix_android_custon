package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/filelock"
)

func TestPurgeOwnershipAndFailureAtomicity(t *testing.T) {
	root := t.TempDir()
	p := NewFilesystemPersistence(root)
	dir := filepath.Join(root, "victim")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := filepath.Join(dir, "body")
	if err := os.WriteFile(body, []byte("history"), 0600); err != nil {
		t.Fatal(err)
	}
	release, err := acquireSessionWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := p.PurgeWithTombstone(t.Context(), "victim", func() error { called = true; return nil }); !errors.Is(err, filelock.ErrHeld) || called {
		t.Fatalf("external owner bypassed: %v %v", err, called)
	}
	release()
	failed := errors.New("registry unavailable")
	if err := p.PurgeWithTombstone(t.Context(), "victim", func() error { return failed }); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if bytes, err := os.ReadFile(body); err != nil || string(bytes) != "history" {
		t.Fatalf("prepare failure changed history: %s %v", bytes, err)
	}
	if err := p.PurgeWithTombstone(t.Context(), "victim", func() error {
		if release, err := acquireSessionWriter(dir); err == nil {
			release()
			return errors.New("writer entered purge")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("purge left body: %v", err)
	}
	if err := p.PurgeWithTombstone(t.Context(), "victim", func() error { return nil }); err != nil {
		t.Fatalf("replay: %v", err)
	}
}

func TestPurgeRejectsSymlinkAndStagingCollision(t *testing.T) {
	for _, scenario := range []string{"symlink", "collision", "unowned-staging"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			p := NewFilesystemPersistence(root)
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "victim")
			if scenario == "symlink" {
				if err := os.Symlink(outside, dir); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if scenario == "unowned-staging" {
					if err := os.Remove(dir); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.MkdirAll(filepath.Join(root, ".purging", "victim"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			if err := p.PurgeWithTombstone(t.Context(), "victim", func() error { called = true; return nil }); err == nil || called {
				t.Fatalf("unsafe purge accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
				t.Fatal("outside data removed")
			}
		})
	}
}
