//go:build !windows

package fileops

import "os"

// OpenReplaceableRead opens a regular source for reading while allowing a
// writer to atomically replace its pathname. Unix files can be unlinked or
// renamed while open, so the ordinary descriptor has the required semantics.
func OpenReplaceableRead(path string) (*os.File, error) {
	return os.Open(path)
}
