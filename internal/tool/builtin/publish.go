package builtin

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"

	"reasonix/internal/fileops"
	"reasonix/internal/tool"
)

// ErrFileChanged is returned when a structured write tool re-reads the target
// and the native identity, freshness metadata, or SHA-256 no longer match.
var ErrFileChanged = errors.New("file changed after it was read")

type fileIdentity struct {
	existed bool
	overlay bool
	sum     [sha256.Size]byte
	mode    os.FileMode
	target  fileops.Target
	version fileops.Version
}

func diskIdentity(path string) (fileIdentity, error) {
	_, id, err := readDiskIdentity(path)
	return id, err
}

// Bytes, identity and freshness metadata must describe the same open source.
func readDiskIdentity(path string) ([]byte, fileIdentity, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fileIdentity{}, nil
		}
		return nil, fileIdentity{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fileIdentity{}, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fileIdentity{}, fmt.Errorf("%s is not a regular file", path)
	}
	target, version := fileops.DiskHandleSnapshot(path, f, fi)
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, fileIdentity{}, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, fileIdentity{}, err
	}
	endTarget, endVersion := fileops.DiskHandleSnapshot(path, f, after)
	current, err := os.Stat(path)
	if err != nil {
		return nil, fileIdentity{}, err
	}
	pathTarget, pathVersion := fileops.DiskSnapshot(path, current)
	if target != endTarget || target != pathTarget || version != endVersion || version != pathVersion {
		return nil, fileIdentity{}, fmt.Errorf("%w: %s", ErrFileChanged, path)
	}
	return b, fileIdentity{existed: true, sum: sha256.Sum256(b), mode: fi.Mode().Perm(), target: target, version: version}, nil
}

func overlayIdentity(content string) fileIdentity {
	return fileIdentity{existed: true, overlay: true, sum: sha256.Sum256([]byte(content))}
}

func (id fileIdentity) equal(other fileIdentity) bool {
	return id.existed == other.existed && id.overlay == other.overlay &&
		id.sum == other.sum && id.mode == other.mode && id.target == other.target && id.version == other.version
}

func (s editSource) assertUnchanged(ctx context.Context, overlay FileOverlay, path string) error {
	now, err := s.currentIdentity(ctx, overlay, path)
	if err != nil {
		return err
	}
	if !s.id.equal(now) {
		return &tool.OperationError{Diagnostic: tool.OperationDiagnostic{Code: tool.FSStaleVersion, Path: path, ExpectedSnapshot: s.readSnapshot(path), Recovery: "read the current file again, then retry"}, Cause: fmt.Errorf("%w: %s", ErrFileChanged, path)}
	}
	return nil
}

func (s editSource) currentIdentity(ctx context.Context, overlay FileOverlay, path string) (fileIdentity, error) {
	if s.overlay && overlay != nil {
		if buffered, ok := overlay.ReadTextFile(ctx, path); ok {
			return overlayIdentity(buffered), nil
		}
	}
	return diskIdentity(path)
}
