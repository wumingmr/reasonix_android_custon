//go:build darwin

package builtin

import (
	"golang.org/x/sys/unix"
	"os"
)

func renameNoReplace(src, dst string) error {
	if err := unix.RenamexNp(src, dst, unix.RENAME_EXCL); err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	return nil
}
