package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/filelock"
	"reasonix/internal/pathidentity"
)

// LockSessionMetaPath serializes a branch-meta read-modify-write cycle across
// goroutines and processes.
func LockSessionMetaPath(path string) (func(), error) {
	lockPath, localKey, err := sessionMetaLockTarget(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionMetaLockWait)
	releaseFile, err := filelock.AcquireModeWithKey(ctx, lockPath, localKey, filelock.ModeExclusive)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("lock session metadata: %w", err)
	}
	return releaseFile, nil
}

func tryLockSessionMetaPath(path string) (func(), bool, error) {
	lockPath, localKey, err := sessionMetaLockTarget(path)
	if err != nil {
		return nil, false, err
	}
	releaseFile, err := filelock.TryAcquireModeWithKey(lockPath, localKey, filelock.ModeExclusive)
	if errors.Is(err, filelock.ErrHeld) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("try lock session metadata: %w", err)
	}
	return releaseFile, true, nil
}

func sessionMetaLockTarget(path string) (string, string, error) {
	identity, err := resolveSessionPathIdentity(path)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(identity.PhysicalPath), 0o755); err != nil {
		return "", "", fmt.Errorf("create session metadata dir: %w", err)
	}
	return sessionMetaLockPath(identity.PhysicalPath), identity.Key + "\x00session-meta", nil
}

func sessionMetaLockPath(path string) string {
	canonical := canonicalSessionSavePath(path)
	digest := sha256.Sum256([]byte(canonical))
	return filepath.Join(filepath.Dir(canonical), "."+hex.EncodeToString(digest[:8])+".meta.lock")
}

func canonicalSessionSavePath(path string) string {
	identity, err := resolveSessionPathIdentity(path)
	if err != nil {
		return ""
	}
	return identity.PhysicalPath
}

// CanonicalSessionPath returns the comparison key shared by session owners,
// lease registries, and save queues. Empty input never becomes the current dir.
func CanonicalSessionPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	identity, err := resolveSessionPathIdentity(path)
	if err != nil {
		return ""
	}
	return identity.Key
}

// SessionSourceKeyFromIdentity hashes an already resolved source identity and
// branch. It performs no filesystem access, so retained metadata snapshots can
// compare the same source keys as Desktop without resolving every source again.
func SessionSourceKeyFromIdentity(pathKey, head string) string {
	sum := sha256.Sum256([]byte(pathKey + "\x00" + head))
	return hex.EncodeToString(sum[:])
}

func resolveSessionPathIdentity(path string) (pathidentity.Identity, error) {
	baseDir := ""
	if !filepath.IsAbs(strings.TrimSpace(path)) {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return pathidentity.Identity{}, err
		}
	}
	return pathidentity.Resolve(path, pathidentity.Options{BaseDir: baseDir, FollowLeaf: true})
}
