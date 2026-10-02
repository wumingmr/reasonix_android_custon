package identitylock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAliasPathsShareLocalIdentity(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasDir := filepath.Join(dir, "alias")
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	release, err := TryAcquire(filepath.Join(realDir, "state.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if aliasRelease, err := TryAcquire(filepath.Join(aliasDir, "state.lock")); !errors.Is(err, ErrHeld) {
		if aliasRelease != nil {
			aliasRelease()
		}
		t.Fatalf("alias acquire error = %v, want ErrHeld", err)
	}
}

func TestAcquireRejectsLinkRedirectAfterIdentityResolution(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	if err := os.Mkdir(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(first, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	originalHook := identityLockAfterResolve
	identityLockAfterResolve = func() {
		identityLockAfterResolve = originalHook
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(second, alias); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { identityLockAfterResolve = originalHook }()

	release, err := Acquire(context.Background(), filepath.Join(alias, "state.lock"))
	if release != nil {
		release()
	}
	if err == nil || err.Error() != "file lock identity changed while acquiring" {
		t.Fatalf("Acquire error = %v, want identity change", err)
	}
	next, nextErr := TryAcquire(filepath.Join(second, "state.lock"))
	if nextErr != nil {
		t.Fatalf("lock remained held after rejected redirect: %v", nextErr)
	}
	next()
}
