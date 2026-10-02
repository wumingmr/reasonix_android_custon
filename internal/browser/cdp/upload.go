package cdp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// uploadRoots decides which files browser_upload may hand to a page. A page is
// untrusted: an unrestricted file input would let any site the agent visits
// receive any file this process can read, so a path outside the session's own
// roots is refused. The tool promises the model that only files the task owns
// can be attached, and this is where that promise is kept.
type uploadRoots struct {
	roots []string
}

// newUploadRoots takes the session's roots plus the executor's artifact
// directory, so a file the agent just downloaded can be attached too. Each
// root is also recorded in its symlink-resolved form, because a platform that
// hands out /tmp for /private/tmp would otherwise refuse the same file
// depending on which spelling the model used.
func newUploadRoots(configured []string, artifacts string) uploadRoots {
	roots := make([]string, 0, 2*(len(configured)+1))
	for _, root := range append(append([]string{}, configured...), artifacts) {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		roots = append(roots, abs)
		if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
			roots = append(roots, real)
		}
	}
	return uploadRoots{roots: roots}
}

// resolve returns the path of an upload candidate, or the reason the model
// cannot attach it. Containment is enforced by os.Root rather than by
// comparing cleaned strings: a root refuses both traversal and a symlink
// leaving it, so a link the agent can write inside the workspace cannot point
// a file input at a private key.
func (u uploadRoots) resolve(path string) (string, string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Sprintf("file %s: %v", path, err)
	}
	for _, root := range u.roots {
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		confined, err := os.OpenRoot(root)
		if err != nil {
			continue
		}
		reason := statRegularFile(confined, rel, path)
		confined.Close()
		if reason != "" {
			return "", reason
		}
		return abs, ""
	}
	return "", fmt.Sprintf("file %s is outside this task's directories, so it cannot be attached to a page", path)
}

// statRegularFile reports why rel cannot be uploaded from confined, or "" when
// it is a readable regular file inside it.
func statRegularFile(confined *os.Root, rel, display string) string {
	info, err := confined.Stat(rel)
	if err != nil {
		return fmt.Sprintf("file %s is not readable: %v", display, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Sprintf("%s is not a regular file", display)
	}
	return ""
}
