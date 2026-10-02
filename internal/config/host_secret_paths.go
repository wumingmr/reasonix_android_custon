package config

import (
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

var hostSecretPaths struct {
	sync.Mutex
	paths []string
}

// RegisterHostSecretPath records a file this process holds a secret in, such
// as serve's --token-file, so runtime sandboxes deny reads of it.
func RegisterHostSecretPath(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	hostSecretPaths.Lock()
	defer hostSecretPaths.Unlock()
	if slices.Contains(hostSecretPaths.paths, path) {
		return
	}
	hostSecretPaths.paths = append(hostSecretPaths.paths, path)
}

// HostSecretReadRoots lists what runtime sandboxes must not read: the remote
// serve state directory, which holds serve launch tokens, and every path
// passed to RegisterHostSecretPath.
func HostSecretReadRoots() []string {
	var out []string
	if dir := RemoteStateDir(); dir != "" {
		out = append(out, dir)
	}
	hostSecretPaths.Lock()
	defer hostSecretPaths.Unlock()
	return append(out, hostSecretPaths.paths...)
}
