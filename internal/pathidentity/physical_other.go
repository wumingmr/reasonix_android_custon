//go:build !windows

package pathidentity

import "path/filepath"

func resolveExistingPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

func platformLinkLoop(error) bool { return false }
