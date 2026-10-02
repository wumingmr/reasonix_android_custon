package main

import (
	"context"
	"os"
	"path/filepath"
	"reasonix/internal/identitylock"
)

func acquireHistoricalSource(ctx context.Context, id string, source historicalSource) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lockDir := filepath.Join(desktopConfigDir(), "desktop", "historical-import-locks")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return nil, err
	}
	lockKey := desktopSourceKey(source.path, source.head)
	release, err := identitylock.TryAcquire(filepath.Join(lockDir, lockKey+".lock"))
	if err != nil {
		return nil, err
	}
	if source.format != "canonical" {
		return release, nil
	}
	ownership, err := acquireHistoricalCanonicalRead(source.path)
	if err != nil {
		release()
		return nil, err
	}
	return func() { ownership(); release() }, nil
}

func acquireHistoricalCanonicalRead(path string) (func(), error) {
	ownership, err := identitylock.TryAcquireMode(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".ownership.lock"), identitylock.ModeShared)
	if err != nil {
		return nil, err
	}
	writer, err := identitylock.TryAcquireMode(filepath.Join(path, "writer.lock"), identitylock.ModeShared)
	if err != nil {
		ownership()
		return nil, err
	}
	return func() { writer(); ownership() }, nil
}
