//go:build !darwin && !windows

package pathidentity

func platformIdentityKey(path string) (string, error) { return path, nil }
