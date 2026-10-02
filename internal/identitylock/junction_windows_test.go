//go:build windows

package identitylock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"reasonix/internal/filelock"
)

func TestJunctionLocksExcludeLegacyAndCurrentWriters(t *testing.T) {
	root := t.TempDir()
	target, alias := filepath.Join(root, "target"), filepath.Join(root, "alias")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", alias, target).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, out)
	}
	legacy, current := filepath.Join(alias, "config.lock"), filepath.Join(target, "config.lock")
	for _, reverse := range []bool{false, true} {
		first, second := filelock.TryAcquire, TryAcquire
		if reverse {
			first, second = TryAcquire, filelock.TryAcquire
		}
		release, err := first(legacy)
		if err != nil {
			t.Fatal(err)
		}
		other, err := second(current)
		release()
		if other != nil {
			other()
		}
		if !errors.Is(err, ErrHeld) {
			t.Fatalf("reverse=%v concurrent writer admitted: %v", reverse, err)
		}
		other, err = second(current)
		if err != nil {
			t.Fatalf("release stranded lock: %v", err)
		}
		other()
	}
}
