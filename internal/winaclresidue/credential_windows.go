//go:build windows

package winaclresidue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const legacyCredentialDenyMask = windows.ACCESS_MASK(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE)

// RepairLegacyCredentialDeny removes the exact current-user DENY RX ACE that
// the retired backend placed on the credential file, provided a dead run's
// marker recorded this path. A mask alone cannot prove origin, so ambiguous
// ACLs and markers with live or unverifiable owners are never modified.
func RepairLegacyCredentialDeny(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	userSID, err := currentProcessUserSIDString()
	if err != nil {
		return err
	}
	legacy, other, err := currentUserDenyACECounts(path, userSID)
	if err != nil {
		return err
	}
	if legacy == 0 {
		return nil
	}
	if other != 0 || legacy != 1 {
		return fmt.Errorf("refusing to alter non-legacy current-user deny ACL on %q", path)
	}
	markers := staleCredentialDenyMarkers(path, canonicalWindowsPath(path))
	if len(markers) == 0 {
		return fmt.Errorf("refusing to alter credential deny ACL without a stale sandbox record on %q", path)
	}

	removeErr := icacls(path, "/remove:d", "*"+userSID, "/C")
	remainingLegacy, remainingOther, verifyErr := currentUserDenyACECounts(path, userSID)
	if verifyErr == nil && remainingLegacy == 0 && remainingOther == 0 {
		sids := residueSIDs()
		for _, marker := range markers {
			sweepMarkerFile(marker, sids)
		}
		return nil
	}
	if removeErr != nil {
		return fmt.Errorf("remove legacy credential deny ACL: %w", removeErr)
	}
	if verifyErr != nil {
		return fmt.Errorf("verify legacy credential deny ACL removal: %w", verifyErr)
	}
	return fmt.Errorf("legacy credential deny ACL remains on %q", path)
}

// staleCredentialDenyMarkers returns the markers of exited runs that recorded
// a deny on path. Any marker with a live or unverifiable owner vetoes repair.
func staleCredentialDenyMarkers(path, canonicalPath string) []string {
	entries, err := os.ReadDir(markerDir())
	if err != nil {
		return nil
	}
	var found []string
	for _, entry := range entries {
		pid, ok := markerOwnerPID(entry.Name())
		if entry.IsDir() || !ok {
			continue
		}
		marker := filepath.Join(markerDir(), entry.Name())
		for _, residue := range readResidueMarker(marker) {
			if residue.kind != residueDeny {
				continue
			}
			if !strings.EqualFold(filepath.Clean(residue.path), filepath.Clean(path)) &&
				!strings.EqualFold(canonicalWindowsPath(residue.path), canonicalPath) {
				continue
			}
			if !processExited(pid) {
				return nil
			}
			found = append(found, marker)
			break
		}
	}
	return found
}

// canonicalWindowsPath expands 8.3 aliases without opening the protected file.
// The original spelling is kept on failure so the exact-path comparison still
// works on volumes without long-name expansion.
func canonicalWindowsPath(path string) string {
	input, err := windows.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return filepath.Clean(path)
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetLongPathName(input, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || n >= uint32(len(buffer)) {
		return filepath.Clean(path)
	}
	return filepath.Clean(windows.UTF16ToString(buffer[:n]))
}

func currentUserDenyACECounts(path, userSID string) (legacy, other int, err error) {
	sid, err := windows.StringToSid(userSID)
	if err != nil {
		return 0, 0, fmt.Errorf("parse current user SID: %w", err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return 0, 0, fmt.Errorf("read credential DACL %q: %w", path, err)
	}
	if sd == nil {
		return 0, 0, nil
	}
	acl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("read credential DACL entries %q: %w", path, err)
	}
	if acl == nil {
		return 0, 0, nil
	}
	for index := range uint32(acl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &ace); err != nil {
			return 0, 0, fmt.Errorf("read credential DACL ACE %d for %q: %w", index, path, err)
		}
		if ace == nil || ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		// SID-wide icacls removal must not erase object or conditional deny
		// entries whose trustee layout differs from a basic deny ACE.
		if ace.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE {
			return 0, 0, fmt.Errorf("refusing to alter unsupported credential ACE type %d on %q", ace.Header.AceType, path)
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !windows.EqualSid(aceSID, sid) {
			continue
		}
		if ace.Header.AceFlags == 0 && ace.Mask == legacyCredentialDenyMask {
			legacy++
		} else {
			other++
		}
	}
	runtime.KeepAlive(sd)
	return legacy, other, nil
}

// ResetCredentialDACL replaces the DACL of the credential store with a
// protected entry that grants only the current user full control. It opens
// the file for WRITE_DAC alone and never reads the existing descriptor, so it
// works when READ_CONTROL is denied and the caller is not the owner. Callers
// reserve it for an explicit save of Reasonix's own credential file after the
// provenance-checked repair could not run.
func ResetCredentialDACL(path string) error {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	// Attributes are readable through the parent directory's list right even
	// while the file itself denies FILE_READ_ATTRIBUTES.
	attrs, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return fmt.Errorf("inspect credential store %q: %w", path, err)
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("refusing to reset the ACL of reparse point %q", path)
	}
	handle, err := openExact(path, windows.WRITE_DAC)
	if err != nil {
		return fmt.Errorf("open credential store for WRITE_DAC: %w", err)
	}
	defer windows.CloseHandle(handle)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if user == nil || user.User.Sid == nil {
		return fmt.Errorf("current process token has no user SID")
	}
	entry := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.STANDARD_RIGHTS_ALL | windows.SPECIFIC_RIGHTS_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee:           windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid)},
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, nil)
	if err != nil {
		return fmt.Errorf("build credential store DACL: %w", err)
	}
	sd, err := windows.NewSecurityDescriptor()
	if err != nil {
		return err
	}
	if err := sd.SetDACL(acl, true, false); err != nil {
		return err
	}
	// Protected: inherited entries from the profile directory must not bring
	// back the trustees the reset is meant to replace.
	if err := sd.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		return err
	}
	if err := windows.SetKernelObjectSecurity(handle, windows.DACL_SECURITY_INFORMATION, sd); err != nil {
		return fmt.Errorf("reset credential store DACL %q: %w", path, err)
	}
	return nil
}
