//go:build windows

package config

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func credentialPlatformInspect(path string, _ os.FileInfo) (string, bool, bool, bool, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", false, false, false, err
	}
	attrs, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return "", false, false, false, err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		return "owner unavailable", false, attrs&windows.FILE_ATTRIBUTE_READONLY != 0, attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return "owner unavailable", false, attrs&windows.FILE_ATTRIBUTE_READONLY != 0, attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return owner.String(), false, attrs&windows.FILE_ATTRIBUTE_READONLY != 0, attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, err
	}
	return fmt.Sprintf("owner %s (current user %s)", owner.String(), user.User.Sid.String()), owner.Equals(user.User.Sid), attrs&windows.FILE_ATTRIBUTE_READONLY != 0, attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

func credentialPlatformRepair(path string, expected os.FileInfo, verify func() error) ([]string, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// No delete sharing: the inspected file cannot be replaced during repair.
	handle, err := windows.CreateFile(ptr, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_WRITE_ATTRIBUTES|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(handle), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(expected, info) {
		return nil, fmt.Errorf("credential file identity changed")
	}
	var basic struct {
		CreationTime, LastAccessTime, LastWriteTime, ChangeTime int64
		FileAttributes                                          uint32
		Padding                                                 uint32
	}
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err != nil {
		return nil, err
	}
	if basic.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, fmt.Errorf("repair refuses reparse points")
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	if sd == nil {
		return nil, fmt.Errorf("security descriptor unavailable")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return nil, fmt.Errorf("credential owner could not be verified")
	}
	entry := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.DELETE,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee:           windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid)},
	}
	// A null DACL already permits access. Replacing it with a one-user ACL
	// would revoke other principals' existing access, outside repair's scope.
	var merged *windows.ACL
	if dacl != nil {
		merged, err = windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, dacl)
		if err != nil {
			return nil, err
		}
	}
	setAttributes := func(attrs uint32) error {
		// Zero timestamps mean unchanged; do not roll back concurrent file data.
		attributes := basic
		attributes.CreationTime, attributes.LastAccessTime, attributes.LastWriteTime, attributes.ChangeTime = 0, 0, 0, 0
		attributes.FileAttributes = attrs
		if attrs == 0 {
			attributes.FileAttributes = windows.FILE_ATTRIBUTE_NORMAL
		}
		return windows.SetFileInformationByHandle(handle, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&attributes)), uint32(unsafe.Sizeof(attributes)))
	}
	if err := setAttributes(basic.FileAttributes &^ windows.FILE_ATTRIBUTE_READONLY); err != nil {
		return nil, err
	}
	rollback := func(cause error) ([]string, error) {
		err := errors.Join(windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil), setAttributes(basic.FileAttributes))
		if err != nil {
			return nil, errors.Join(fmt.Errorf("repair failed: %w", cause), fmt.Errorf("rollback failed: %w", err))
		}
		return nil, fmt.Errorf("repair failed: %w; changed attributes were restored", cause)
	}
	if merged != nil {
		if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, merged, nil); err != nil {
			return rollback(err)
		}
	}
	if err := verify(); err != nil {
		return rollback(err)
	}
	return []string{"cleared read-only state and added current-user access without removing existing ACL entries"}, nil
}
