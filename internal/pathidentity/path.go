// Package pathidentity separates paths used for filesystem access from keys
// used to compare filesystem identity.
package pathidentity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
)

const Version = 3

type ErrorKind string

const (
	ErrorInvalid     ErrorKind = "invalid"
	ErrorPermission  ErrorKind = "permission"
	ErrorLinkLoop    ErrorKind = "link_loop"
	ErrorUnavailable ErrorKind = "unavailable"
	ErrorUnknown     ErrorKind = "unknown"
)

type Error struct {
	Kind  ErrorKind
	Stage string
	Path  string
	Err   error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("resolve path identity (%s, %s): %v", e.Stage, e.Kind, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

type Options struct {
	// BaseDir is required when path is relative. It must itself be absolute.
	BaseDir string
	// FollowLeaf resolves the final directory entry. Leave it false for
	// rename/delete operations that act on the entry rather than its target.
	FollowLeaf bool
}

type Identity struct {
	AccessPath   string
	PhysicalPath string
	Key          string
}

func Resolve(path string, options Options) (Identity, error) {
	access, err := absoluteAccessPath(path, options.BaseDir)
	if err != nil {
		return Identity{}, err
	}
	physical, err := resolvePhysicalPath(access, options.FollowLeaf)
	if err != nil {
		return Identity{}, err
	}
	key, err := platformIdentityKey(physical)
	if err != nil {
		return Identity{}, classify("compare", physical, err)
	}
	return Identity{AccessPath: access, PhysicalPath: physical, Key: filepath.Clean(key)}, nil
}

func Same(a, b string, options Options) (bool, error) {
	return NewMatcher(options, nil).Same(a, b)
}

func statForMode(path string, followLeaf bool) (os.FileInfo, error) {
	if followLeaf {
		return os.Stat(path)
	}
	return os.Lstat(path)
}

func absoluteAccessPath(path, baseDir string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", &Error{Kind: ErrorInvalid, Stage: "input", Path: path, Err: errors.New("path is empty")}
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		baseDir = strings.TrimSpace(baseDir)
		if baseDir == "" || !filepath.IsAbs(baseDir) {
			return "", &Error{Kind: ErrorInvalid, Stage: "input", Path: path, Err: errors.New("relative path requires an absolute base directory")}
		}
		path = filepath.Join(filepath.Clean(baseDir), path)
	}
	return filepath.Clean(path), nil
}

func resolvePhysicalPath(access string, followLeaf bool) (string, error) {
	target, leaf := access, ""
	if !followLeaf {
		parent := filepath.Dir(access)
		if parent != access {
			target, leaf = parent, filepath.Base(access)
		}
	}
	resolved, err := resolveThroughExistingAncestor(target)
	if err != nil {
		return "", err
	}
	if leaf != "" {
		resolved = filepath.Join(resolved, leaf)
	}
	return filepath.Clean(resolved), nil
}

func resolveThroughExistingAncestor(path string) (string, error) {
	return resolveThroughExistingAncestorWith(path, resolveExistingPath)
}

func resolveThroughExistingAncestorWith(path string, evalSymlinks func(string) (string, error)) (string, error) {
	current := filepath.Clean(path)
	missing := make([]string, 0, 4)
	for {
		// Inspect existence before resolving links. A missing entry may be
		// created by another lock contender; that is not a dangling link.
		_, err := os.Lstat(current)
		if err == nil {
			resolved, resolveErr := evalSymlinks(current)
			if resolveErr != nil {
				return "", classify("physical", current, resolveErr)
			}
			for _, part := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, part)
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", classify("physical", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", &Error{Kind: ErrorUnavailable, Stage: "physical", Path: path, Err: err}
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func classify(stage, path string, err error) error {
	kind := ErrorUnknown
	switch {
	case errors.Is(err, os.ErrPermission):
		kind = ErrorPermission
	case errors.Is(err, syscall.ELOOP) || platformLinkLoop(err) || strings.Contains(strings.ToLower(err.Error()), "too many links"):
		kind = ErrorLinkLoop
	case errors.Is(err, os.ErrNotExist):
		kind = ErrorUnavailable
	}
	return &Error{Kind: kind, Stage: stage, Path: path, Err: err}
}

// Canonical is the frozen v1 compatibility key used by historical locks.
// New identity-sensitive code must use Resolve so errors are not discarded.
func Canonical(path string) string {
	key := filepath.Clean(strings.TrimSpace(path))
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	key = legacyResolvePathThroughExistingAncestor(key)
	if runtime.GOOS == "windows" {
		if strings.HasPrefix(strings.ToUpper(key), `\\?\UNC\`) {
			key = `\\` + key[len(`\\?\UNC\`):]
		} else {
			key = strings.TrimPrefix(key, `\\?\`)
		}
		key = strings.ToLower(key)
	}
	return key
}

func legacyResolvePathThroughExistingAncestor(path string) string {
	current := filepath.Clean(path)
	missing := make([]string, 0, 4)
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for _, part := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, part)
			}
			return resolved
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
