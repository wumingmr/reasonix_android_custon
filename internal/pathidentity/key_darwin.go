//go:build darwin

package pathidentity

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const pathconfCaseSensitive = 11

var systemVolume = darwinVolume{
	fsType: func(dir string) (string, error) {
		var stat unix.Statfs_t
		if err := unix.Statfs(dir, &stat); err != nil {
			return "", err
		}
		return strings.TrimRight(string(stat.Fstypename[:]), "\x00"), nil
	},
	caseSensitive: func(dir string) (int, error) {
		return syscall.Pathconf(dir, pathconfCaseSensitive)
	},
}

func platformIdentityKey(path string) (string, error) {
	parent, err := closestExistingDirectory(path)
	if err != nil {
		return "", err
	}
	return darwinIdentityKeyBy(path, parent, systemVolume)
}

func closestExistingDirectory(path string) (string, error) {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil && info.IsDir() {
			return current, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if filepath.Dir(current) == current {
			return "", os.ErrNotExist
		}
	}
}
