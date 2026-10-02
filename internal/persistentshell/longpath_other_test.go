//go:build !windows

package persistentshell

// longPath is a no-op off Windows: 8.3 short-name aliases do not exist there.
func longPath(path string) string { return path }
