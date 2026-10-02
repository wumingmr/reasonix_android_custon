// Package identitylock combines filesystem path identity with filelock's
// process-local queue and cross-process advisory lock.
package identitylock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/filelock"
	"reasonix/internal/pathidentity"
)

type Mode = filelock.Mode

const (
	ModeExclusive = filelock.ModeExclusive
	ModeShared    = filelock.ModeShared
)

var ErrHeld = filelock.ErrHeld

var identityLockAfterResolve = func() {}

func Acquire(ctx context.Context, path string) (func(), error) {
	return AcquireMode(ctx, path, ModeExclusive)
}

func AcquireMode(ctx context.Context, path string, mode Mode) (func(), error) {
	accessPath, key, err := resolve(path)
	if err != nil {
		return nil, err
	}
	identityLockAfterResolve()
	release, err := filelock.AcquireModeWithKey(ctx, accessPath, key, mode)
	return revalidate(accessPath, key, release, err)
}

func AcquireWithExternalTimeout(ctx context.Context, path string, timeout time.Duration) (func(), error) {
	accessPath, key, err := resolve(path)
	if err != nil {
		return nil, err
	}
	identityLockAfterResolve()
	release, err := filelock.AcquireWithExternalTimeoutAndKey(ctx, accessPath, key, timeout)
	return revalidate(accessPath, key, release, err)
}

func TryAcquire(path string) (func(), error) {
	return TryAcquireMode(path, ModeExclusive)
}

func TryAcquireMode(path string, mode Mode) (func(), error) {
	accessPath, key, err := resolve(path)
	if err != nil {
		return nil, err
	}
	identityLockAfterResolve()
	release, err := filelock.TryAcquireModeWithKey(accessPath, key, mode)
	return revalidate(accessPath, key, release, err)
}

func revalidate(accessPath, expectedKey string, release func(), acquireErr error) (func(), error) {
	if acquireErr != nil {
		return nil, acquireErr
	}
	_, actualKey, err := resolve(accessPath)
	if err != nil {
		release()
		return nil, fmt.Errorf("revalidate file lock identity: %w", err)
	}
	if actualKey != expectedKey {
		release()
		return nil, fmt.Errorf("file lock identity changed while acquiring")
	}
	return release, nil
}

func resolve(path string) (string, string, error) {
	baseDir := ""
	if !filepath.IsAbs(strings.TrimSpace(path)) {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return "", "", fmt.Errorf("resolve file lock identity: %w", err)
		}
	}
	identity, err := pathidentity.Resolve(path, pathidentity.Options{BaseDir: baseDir, FollowLeaf: true})
	if err != nil {
		return "", "", fmt.Errorf("resolve file lock identity: %w", err)
	}
	return identity.AccessPath, identity.Key, nil
}
