package repair

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/filelock"
)

func TestRepairMutationLockRejectsLeafRedirectBeforeWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.Mkdir(first, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "current")
	if err := os.Symlink(first, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	original := repairMutationBeforeLock
	repairMutationBeforeLock = func([]string) {
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(second, link); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { repairMutationBeforeLock = original })
	unlock, err := lockRepairMutations(link)
	if unlock != nil {
		unlock()
	}
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("error = %v", err)
	}
}

func TestRepairMutationLockAllowsPriorHolderRegularFileReplacement(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	target := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := repairMutationBeforeLock
	repairMutationBeforeLock = func([]string) {
		replacement := target + ".new"
		if err := os.WriteFile(replacement, []byte("after"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, target); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { repairMutationBeforeLock = original })
	unlock, err := lockRepairMutations(target)
	if err != nil {
		t.Fatalf("lock after regular-file replacement: %v", err)
	}
	unlock()
}

func TestRepairMutationRejectsRedirectEvenWithReusedNativeIdentity(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "current")
	if err := os.Symlink(filepath.Join(root, "first"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	targets, _, _, err := repairMutationTargets([]string{link})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "second"), link); err != nil {
		t.Fatal(err)
	}
	// Force the identity comparison to pass, as when unlink/recreate reuses
	// a filesystem inode; the saved destination must still reject the write.
	targets[0].info, err = os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := revalidateRepairMutationTargets(targets); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("redirect with reused identity accepted: %v", err)
	}
}

func TestRepairMutationLockContendsWithLegacyLockBothDirections(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	target := filepath.Join(t.TempDir(), "state.json")
	legacyKey := legacyCanonicalRepairPath(target)
	digest := sha256.Sum256([]byte(legacyKey))
	legacyLock := filepath.Join(config.RepairMutationLockDir(), fmt.Sprintf("%x.lock", digest))
	if err := os.MkdirAll(filepath.Dir(legacyLock), 0o700); err != nil {
		t.Fatal(err)
	}
	holdLegacy, err := filelock.Acquire(context.Background(), legacyLock)
	if err != nil {
		t.Fatal(err)
	}
	if unlock, err := LockRepairMutationsTimeout(80*time.Millisecond, target); !errors.Is(err, context.DeadlineExceeded) {
		if unlock != nil {
			unlock()
		}
		holdLegacy()
		t.Fatalf("new writer bypassed legacy lock: %v", err)
	}
	holdLegacy()

	holdNew, err := LockRepairMutations(target)
	if err != nil {
		t.Fatal(err)
	}
	defer holdNew()
	if release, err := filelock.TryAcquire(legacyLock); !errors.Is(err, filelock.ErrHeld) {
		if release != nil {
			release()
		}
		t.Fatalf("legacy writer bypassed new lock set: %v", err)
	}
}
