package pathidentity

import (
	"errors"
	"strings"
	"syscall"

	"golang.org/x/text/unicode/norm"
)

// darwinVolume answers the two questions the darwin identity key asks of the
// volume holding a directory. It is platform-neutral so the rules are testable
// on every host; key_darwin.go binds it to statfs(2) and pathconf(2).
type darwinVolume struct {
	fsType        func(dir string) (string, error)
	caseSensitive func(dir string) (int, error)
}

func darwinIdentityKeyBy(path, parent string, volume darwinVolume) (string, error) {
	// A volume whose type cannot be read is not known to normalize names, so
	// the path is kept as spelled rather than failing the whole lookup.
	if fsType, err := volume.fsType(parent); err == nil && (fsType == "apfs" || fsType == "hfs") {
		path = norm.NFD.String(path)
	}
	caseSensitive, err := volume.caseSensitive(parent)
	if err != nil {
		// FUSE volumes (sshfs over macFUSE) do not implement
		// _PC_CASE_SENSITIVE; an undeclared answer keeps names distinct.
		if !pathconfUndeclared(err) {
			return "", err
		}
		caseSensitive = 1
	}
	if caseSensitive == 0 {
		path = strings.ToLower(path)
	}
	return path, nil
}

func pathconfUndeclared(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP)
}
