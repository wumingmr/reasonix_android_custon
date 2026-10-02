//go:build windows

package pathidentity

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The kernel reports exhausted reparse traversal as CANT_RESOLVE_FILENAME,
// rather than the synthetic syscall.ELOOP used by Go's userspace walker.
func platformLinkLoop(err error) bool {
	return errors.Is(err, windows.ERROR_CANT_RESOLVE_FILENAME)
}

// resolveExistingPath asks the kernel to follow all reparse points, including
// junctions. EvalSymlinks does not follow mount points reported as ModeIrregular:
// it can return the alias unchanged for a leaf and ENOTDIR for its descendants.
// This must be the primary resolver, not merely a fallback after an error.
func resolveExistingPath(path string) (string, error) {
	handle, err := openPhysicalPath(path)
	if err != nil {
		return "", fmt.Errorf("open physical path: %w", err)
	}
	defer windows.CloseHandle(handle)
	for size := uint32(256); size <= 65536; {
		buf := make([]uint16, size)
		n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], size, 0)
		if err != nil {
			return "", fmt.Errorf("get final physical path: %w", err)
		}
		if n < size {
			return stripExtendedPrefix(windows.UTF16ToString(buf[:n])), nil
		}
		if n >= 65536 {
			break
		}
		size = n + 1
	}
	return "", fmt.Errorf("final physical path exceeds 65536 UTF-16 units")
}

// NtCreateFile avoids CreateFile's implicit SYNCHRONIZE requirement. Owners
// denied file attributes can still query security metadata via READ_CONTROL.
func openPhysicalPath(path string) (windows.Handle, error) {
	name, err := windows.NewNTUnicodeString(ntPhysicalPath(path))
	if err != nil {
		return 0, err
	}
	oa := windows.OBJECT_ATTRIBUTES{ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var handle windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	open := func(access uint32) error {
		return windows.NtCreateFile(&handle, access, &oa, &iosb, nil, 0,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			windows.FILE_OPEN, windows.FILE_OPEN_FOR_BACKUP_INTENT, 0, 0)
	}
	err = open(windows.FILE_READ_ATTRIBUTES)
	if errors.Is(err, windows.STATUS_ACCESS_DENIED) {
		err = open(windows.READ_CONTROL)
	}
	var status windows.NTStatus
	if errors.As(err, &status) {
		err = status.Errno()
	}
	return handle, err
}

func ntPhysicalPath(path string) string {
	extended := extendedWindowsPath(path)
	return `\??\` + extended[4:]
}

// Native Windows calls do not apply the long-path handling performed by os.
// Inputs here are already absolute and clean; preserve existing device paths.
func extendedWindowsPath(path string) string {
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + path[2:]
	}
	if filepath.IsAbs(path) {
		return `\\?\` + path
	}
	return path
}
