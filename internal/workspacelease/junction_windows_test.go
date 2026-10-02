//go:build windows

package workspacelease

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/filelock"
)

func TestJunctionWorkspaceRetainsOldAliasLock(t *testing.T) {
	root := t.TempDir()
	target, alias := filepath.Join(root, "target"), filepath.Join(root, "alias")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", alias, target).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, out)
	}
	lockDir := filepath.Join(root, "locks")
	owner, err := New(alias, lockDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Use the historical filesystem observation, including 8.3 expansion of
	// the runner's temporary directory. The raw input is not the old lock key.
	oldRoot, err := filepath.EvalSymlinks(alias)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := workspaceLockPath(lockDir, compatibilityIdentityPath(nearestGitWorktreeRoot(oldRoot)))
	if owner.lockPath != oldPath {
		t.Fatalf("legacy lock changed: %q != %q", owner.lockPath, oldPath)
	}
	for _, group := range []bool{false, true} {
		releaseOld, err := filelock.TryAcquire(oldPath)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		var release func()
		if group {
			release, err = HoldWriteRoots(ctx, lockDir, alias)
		} else {
			release, err = owner.HoldWrite(ctx)
		}
		cancel()
		releaseOld()
		if release != nil {
			release()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("group=%v ignored old alias writer: %v", group, err)
		}
	}
	release, err := owner.HoldWrite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	legacy, err := filelock.TryAcquire(oldPath)
	if legacy != nil {
		legacy()
	}
	if !errors.Is(err, filelock.ErrHeld) {
		t.Fatalf("old writer ignored current holder: %v", err)
	}
}
