//go:build !windows

package config

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func credentialPlatformInspect(_ string, info os.FileInfo) (string, bool, bool, bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "owner unavailable", false, info.Mode().Perm()&0o200 == 0, false, fmt.Errorf("owner information unavailable")
	}
	current := int(stat.Uid) == os.Geteuid()
	return fmt.Sprintf("uid %d (current uid %d)", stat.Uid, os.Geteuid()), current, info.Mode().Perm()&0o200 == 0, false, nil
}

func credentialPlatformRepair(path string, expected os.FileInfo, verify func() error) ([]string, error) {
	// Mutate the inspected inode, never a path that can be replaced between
	// inspection, chmod and rollback. No-follow and nonblocking reject links/FIFOs.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(expected, info) {
		return nil, fmt.Errorf("credential file identity changed")
	}
	_, owned, _, _, err := credentialPlatformInspect(path, info)
	if err != nil || !owned {
		return nil, fmt.Errorf("credential owner could not be verified")
	}
	before := info.Mode().Perm()
	after := before | 0o600
	if err := f.Chmod(after); err != nil {
		return nil, err
	}
	actions := []string{}
	if after != before {
		actions = append(actions, "added owner read/write permission")
	}
	if err := verify(); err != nil {
		if rollbackErr := f.Chmod(before); rollbackErr != nil {
			return nil, errors.Join(
				fmt.Errorf("verification failed: %w", err),
				fmt.Errorf("rollback failed: %w", rollbackErr),
			)
		}
		return nil, fmt.Errorf("verification failed: %w; changed attributes were restored", err)
	}
	return actions, nil
}
