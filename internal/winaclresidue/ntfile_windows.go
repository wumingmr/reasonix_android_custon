//go:build windows

package winaclresidue

import (
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// openExact opens path with exactly the requested access rights. CreateFile
// silently adds SYNCHRONIZE and FILE_READ_ATTRIBUTES, both of which a legacy
// DENY RX entry removes, so a WRITE_DAC- or DELETE-only open has to go
// through NtCreateFile. Reparse points are opened as themselves, never
// followed.
func openExact(path string, access uint32) (windows.Handle, error) {
	name, err := windows.NewNTUnicodeString(ntPath(path))
	if err != nil {
		return 0, err
	}
	oa := windows.OBJECT_ATTRIBUTES{ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var (
		handle windows.Handle
		iosb   windows.IO_STATUS_BLOCK
	)
	err = windows.NtCreateFile(&handle, access, &oa, &iosb, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("open %q with access 0x%x: %w", path, access, err)
	}
	return handle, nil
}

// ntPath converts an absolute Win32 path to the NT object namespace.
func ntPath(path string) string {
	clean := filepath.Clean(path)
	if rest, ok := strings.CutPrefix(clean, `\\`); ok {
		return `\??\UNC\` + rest
	}
	return `\??\` + clean
}

// fileRenameInformation mirrors FILE_RENAME_INFORMATION; the name is stored
// inline after the header, so callers allocate the struct plus the name.
type fileRenameInformation struct {
	ReplaceIfExists uint8
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

// RenameLockedFile moves path to target through a DELETE-only handle so a
// deny that removes read rights cannot block the move; MoveFileEx would also
// ask for SYNCHRONIZE and fail. The target must not exist.
func RenameLockedFile(path, target string) error {
	handle, err := openExact(path, windows.DELETE)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	name, err := windows.UTF16FromString(ntPath(target))
	if err != nil {
		return err
	}
	name = name[:len(name)-1]
	header := unsafe.Offsetof(fileRenameInformation{}.FileName)
	size := int(header) + len(name)*2
	buffer := make([]uint64, (size+7)/8)
	info := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.FileNameLength = uint32(len(name) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Add(unsafe.Pointer(&buffer[0]), header)), len(name)), name)
	var iosb windows.IO_STATUS_BLOCK
	if err := windows.NtSetInformationFile(handle, &iosb, (*byte)(unsafe.Pointer(&buffer[0])), uint32(size), windows.FileRenameInformation); err != nil {
		return fmt.Errorf("rename %q to %q: %w", path, target, err)
	}
	return nil
}
