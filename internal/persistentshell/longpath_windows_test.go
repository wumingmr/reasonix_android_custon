package persistentshell

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// longPath expands 8.3 short-name aliases (e.g. HELLOF~1 when TEMP is a short
// path): PowerShell's Set-Location cannot enter a directory spelled with one.
// The original spelling is kept on failure, so the harness still gets a real
// directory on volumes without long-name expansion.
func longPath(path string) string {
	cleaned := filepath.Clean(path)
	input, err := windows.UTF16PtrFromString(cleaned)
	if err != nil {
		return cleaned
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetLongPathName(input, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || n >= uint32(len(buffer)) {
		return cleaned
	}
	return windows.UTF16ToString(buffer[:n])
}
