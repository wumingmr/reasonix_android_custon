//go:build !windows

package fileops

import "os"

func diskNativeSnapshot(string) (string, []string) { return "", nil }
func diskNativeHandleSnapshot(*os.File) (string, []string) {
	return "", nil
}
