package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// Cold metadata reads the durable log once, one complete transaction at a time.
// Repeated small pages would re-read up to 256 commits from sparse checkpoints.
func (h *readHandle) scanCatalog(ctx context.Context, apply func(Commit) error) error {
	if h == nil {
		return os.ErrClosed
	}
	h.mu.Lock()
	closed, dir := h.closed, h.dir
	h.mu.Unlock()
	if closed {
		return os.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	manifest, err := readStoredManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return err
	}
	file, err := os.Open(logPathForManifest(dir, manifest))
	if err != nil {
		return err
	}
	defer file.Close()
	var applyErr error
	visit := func(_ int64, commit Commit) bool {
		if ctx.Err() != nil {
			return false
		}
		applyErr = apply(commit)
		return applyErr == nil
	}
	if manifest.Codec == Codec {
		err = scanV4CommitFile(ctx, file, 0, 1, contentStoreForSessionDir(dir), nil, visit)
	} else {
		err = scanCommitFileCodec(file, 0, 1, manifest.Codec, nil, visit)
	}
	return errors.Join(err, applyErr, ctx.Err())
}
