//go:build !darwin && !linux && !windows

package builtin

import "fmt"

func renameNoReplace(src, dst string) error {
	return fmt.Errorf("atomic no-replace move is unavailable on this platform")
}
