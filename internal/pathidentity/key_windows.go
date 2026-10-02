//go:build windows

package pathidentity

import (
	"errors"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformIdentityKey(path string) (string, error) {
	return windowsIdentityKeyBy(path, directoryCaseInsensitive)
}

func windowsIdentityKeyBy(path string, caseForDirectory func(string) (bool, bool, error)) (string, error) {
	path = stripExtendedPrefix(path)
	volume := filepath.VolumeName(path)
	if volume == "" {
		return "", errors.New("windows path has no volume")
	}
	current := volume + string(filepath.Separator)
	identity := strings.ToLower(current)
	caseInsensitive := true
	rest := strings.TrimLeft(path[len(volume):], `\/`)
	for _, component := range strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' }) {
		insensitive, exists, err := caseForDirectory(current)
		if err != nil {
			return "", err
		}
		if exists {
			caseInsensitive = insensitive
		}
		identityComponent := component
		if caseInsensitive {
			identityComponent = strings.ToLower(component)
		}
		identity = filepath.Join(identity, identityComponent)
		current = filepath.Join(current, component)
	}
	return filepath.Clean(identity), nil
}

func directoryCaseInsensitive(path string) (insensitive, exists bool, err error) {
	name, err := windows.UTF16PtrFromString(extendedWindowsPath(path))
	if err != nil {
		return false, false, err
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return false, false, nil
		}
		return false, false, err
	}
	defer windows.CloseHandle(handle)
	var iosb windows.IO_STATUS_BLOCK
	var flags uint32
	err = windows.NtQueryInformationFile(
		handle,
		&iosb,
		(*byte)(unsafe.Pointer(&flags)),
		uint32(unsafe.Sizeof(flags)),
		windows.FileCaseSensitiveInformation,
	)
	if caseSensitivityQueryUnsupported(err) {
		return true, true, nil
	}
	if err != nil {
		return false, false, err
	}
	return flags&windows.FILE_CS_FLAG_CASE_SENSITIVE_DIR == 0, true, nil
}

func caseSensitivityQueryUnsupported(err error) bool {
	var status windows.NTStatus
	return errors.As(err, &status) && (status == windows.STATUS_INVALID_INFO_CLASS ||
		status == windows.STATUS_INVALID_PARAMETER ||
		status == windows.STATUS_NOT_SUPPORTED)
}

func stripExtendedPrefix(path string) string {
	if strings.HasPrefix(strings.ToUpper(path), `\\?\UNC\`) {
		return `\\` + path[len(`\\?\UNC\`):]
	}
	// A volume GUID has no equivalent spelling without its device prefix.
	if len(path) >= 7 && strings.HasPrefix(path, `\\?\`) && path[5] == ':' && path[6] == '\\' {
		return path[4:]
	}
	return path
}
