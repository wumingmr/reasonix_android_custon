package session

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Owned artifacts are independent copies, never links to the parent. History
// references remain relative to the session that owns their imported data.
func copyOwnedSessionFiles(ctx context.Context, source, target string) error {
	for _, name := range []string{"attachments", "assets", "legacy"} {
		root := filepath.Join(source, name)
		if _, err := os.Lstat(root); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			destination := filepath.Join(target, relative)
			if entry.IsDir() {
				return os.MkdirAll(destination, 0700)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("session: owned artifact is not a regular file: %s", relative)
			}
			return copySessionFile(ctx, path, destination, 0600)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
