//go:build windows

package main

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

func nativePath(path string) outcome {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return failureOutcome(err)
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return failureOutcome(fmt.Errorf("CreateFileW OPEN_EXISTING: %w", err))
	}
	defer windows.CloseHandle(handle)
	result := outcome{OK: true}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		result.Errors = failureOutcome(fmt.Errorf("GetFileInformationByHandle: %w", err)).Errors
	} else {
		result.Volume = fmt.Sprintf("%08x", info.VolumeSerialNumber)
		result.FileID = fmt.Sprintf("%08x%08x", info.FileIndexHigh, info.FileIndexLow)
	}
	for size := uint32(256); size <= 65536; {
		buf := make([]uint16, size)
		n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], size, 0)
		if err != nil {
			return failureOutcome(fmt.Errorf("GetFinalPathNameByHandleW: %w", err))
		}
		if n < size {
			result.Path = normalizeNativePath(windows.UTF16ToString(buf[:n]))
			return result
		}
		if n >= 65536 {
			break
		}
		size = n + 1
	}
	return failureOutcome(fmt.Errorf("GetFinalPathNameByHandleW exceeds 65536 UTF-16 units"))
}

func normalizeNativePath(path string) string {
	if strings.HasPrefix(strings.ToUpper(path), `\\?\UNC\`) {
		return `\\` + path[8:]
	}
	if len(path) >= 7 && strings.HasPrefix(path, `\\?\`) && path[5] == ':' && path[6] == '\\' {
		return path[4:]
	}
	return path
}
