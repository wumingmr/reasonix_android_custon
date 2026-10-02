//go:build windows

package fileops

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsFileBasicInfo struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	Attributes     uint32
	Reserved       uint32
}

func diskNativeSnapshot(path string) (string, []string) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", nil
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", nil
	}
	defer windows.CloseHandle(h)
	return windowsSnapshot(h)
}

func diskNativeHandleSnapshot(file *os.File) (string, []string) {
	if file == nil {
		return "", nil
	}
	return windowsSnapshot(windows.Handle(file.Fd()))
}

func windowsSnapshot(h windows.Handle) (string, []string) {
	var byHandle windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &byHandle); err != nil {
		return "", nil
	}
	identity := fmt.Sprintf("volume=%d,fileindex=%08x%08x", byHandle.VolumeSerialNumber, byHandle.FileIndexHigh, byHandle.FileIndexLow)
	meta := []string{
		fmt.Sprintf("win.volume=%d", byHandle.VolumeSerialNumber),
		fmt.Sprintf("win.fileindex=%08x%08x", byHandle.FileIndexHigh, byHandle.FileIndexLow),
		fmt.Sprintf("win.links=%d", byHandle.NumberOfLinks),
		fmt.Sprintf("win.attributes=%d", byHandle.FileAttributes),
	}
	var basic windowsFileBasicInfo
	if err := windows.GetFileInformationByHandleEx(h, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err == nil {
		meta = append(meta,
			fmt.Sprintf("win.creation=%d", basic.CreationTime),
			fmt.Sprintf("win.lastwrite=%d", basic.LastWriteTime),
			fmt.Sprintf("win.change=%d", basic.ChangeTime),
			fmt.Sprintf("win.basic_attributes=%d", basic.Attributes),
		)
	}
	return identity, meta
}
