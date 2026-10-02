package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"reasonix/internal/fileutil"
	filelock "reasonix/internal/identitylock"
)

// PurgeWithTombstone keeps directory ownership across writer-lock release and
// rename (required on Windows). The callback durably withdraws the identity.
// The deterministic staging directory makes interrupted removal replayable.
func (p *FilesystemPersistence) PurgeWithTombstone(ctx context.Context, id string, prepare func() error) error {
	return p.purgeWithTombstone(ctx, id, prepare, func(string) {})
}

// checkpoint belongs to one invocation; tests terminate a child process here
// to exercise real lock release and restart, without process-global hooks.
func (p *FilesystemPersistence) purgeWithTombstone(ctx context.Context, id string, prepare func() error, checkpoint func(string)) error {
	source, err := p.sessionDir(id, false)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	releaseDirectory, err := filelock.TryAcquire(directoryOwnershipPath(source))
	if err != nil {
		return err
	}
	defer releaseDirectory()
	staged := filepath.Join(p.Root, ".purging", id)
	receipt := staged + ".receipt"
	proof := "reasonix-session-purge-v1\n" + id + "\n"
	if err := validatePurgeDirectories(p.Root, source, staged, receipt, proof); err != nil {
		return err
	}
	if _, err := os.Lstat(source); err == nil {
		if _, err := os.Lstat(staged); err == nil {
			return errors.New("session purge staging collision")
		} else if !os.IsNotExist(err) {
			return err
		}
		release, err := filelock.TryAcquire(filepath.Join(source, "writer.lock"))
		if err != nil {
			return err
		}
		checkpoint("before-tombstone")
		if err := prepare(); err != nil {
			release()
			return err
		}
		checkpoint("after-tombstone")
		if err := os.MkdirAll(filepath.Dir(staged), 0700); err != nil {
			release()
			return err
		}
		if err := fileutil.AtomicCreateFile(receipt, []byte(proof), 0600); err != nil && !errors.Is(err, os.ErrExist) {
			release()
			return err
		}
		release()
		checkpoint("before-rename")
		if err := os.Rename(source, staged); err != nil {
			return err
		}
		checkpoint("after-rename")
	} else if !os.IsNotExist(err) {
		return err
	} else {
		if _, err := os.Lstat(staged); err == nil {
			if _, err := os.Lstat(receipt); err != nil {
				return errors.New("unowned session purge staging directory")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := prepare(); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(staged); err != nil {
		return err
	}
	checkpoint("after-content-removal")
	if err := os.RemoveAll(filepath.Join(p.Root, ".query-cache", id)); err != nil {
		return err
	}
	if err := os.Remove(receipt); err != nil && !os.IsNotExist(err) {
		return err
	}
	checkpoint("after-cleanup")
	return nil
}

// AcquireMaintenance excludes an external writer without opening, recovering,
// or changing a cold session. The caller must not already own its runtime.
func (p *FilesystemPersistence) AcquireMaintenance(sessionID string) (func(), error) {
	dir, err := p.sessionDir(sessionID, true)
	if err != nil {
		return nil, err
	}
	release, err := acquireSessionWriter(dir)
	if errors.Is(err, filelock.ErrHeld) {
		return nil, fmt.Errorf("%w", ErrWriterOwned)
	}
	return release, err
}

func validatePurgeDirectories(root, source, staged, receipt, proof string) error {
	for _, path := range []string{root, filepath.Dir(staged), staged, source, filepath.Join(root, ".query-cache")} {
		info, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("unsafe session maintenance directory")
		}
	}
	if info, err := os.Lstat(receipt); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("unsafe session purge receipt")
		}
		body, err := os.ReadFile(receipt)
		if err != nil || string(body) != proof {
			return errors.New("session purge receipt mismatch")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}
