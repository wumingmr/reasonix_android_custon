//go:build !windows

package winaclresidue

import (
	"errors"
	"os"
)

// SweepStaleMarkers is a no-op outside Windows.
func SweepStaleMarkers() {}

// RepairLegacyCredentialDeny is a no-op outside Windows.
func RepairLegacyCredentialDeny(string) error { return nil }

// ResetCredentialDACL has no equivalent outside Windows.
func ResetCredentialDACL(string) error { return errors.ErrUnsupported }

// RenameLockedFile is an ordinary rename outside Windows.
func RenameLockedFile(path, target string) error { return os.Rename(path, target) }
