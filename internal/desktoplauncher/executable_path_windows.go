//go:build windows

package desktoplauncher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const maxFinalPathUTF16 = 1 << 16

type launchLocation string

const (
	launchLocationLocal       launchLocation = "local"
	launchLocationUNC         launchLocation = "unc"
	launchLocationRemoteDrive launchLocation = "remote_drive"
)

// resolveExecutablePath opens the launcher and asks Windows for the final DOS
// path represented by that handle. Unlike filepath.EvalSymlinks, this resolves
// directory junctions such as Scoop's stable current entry.
func resolveExecutablePath(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open executable: %w", err)
	}
	defer file.Close()

	handle := windows.Handle(file.Fd())
	size := uint32(256)
	for {
		buf := make([]uint16, size)
		n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], size, 0)
		if err != nil {
			return "", fmt.Errorf("get final executable path: %w", err)
		}
		if n < size {
			return normalizeFinalWindowsPath(windows.UTF16ToString(buf[:n])), nil
		}
		if n >= maxFinalPathUTF16 {
			return "", fmt.Errorf("get final executable path: required buffer is too large: %d", n)
		}
		size = n + 1
	}
}

func normalizeFinalWindowsPath(path string) string {
	const (
		extendedPrefix = `\\?\`
		extendedUNC    = `\\?\UNC\`
	)
	if len(path) >= len(extendedUNC) && strings.EqualFold(path[:len(extendedUNC)], extendedUNC) {
		return `\\` + path[len(extendedUNC):]
	}
	if len(path) >= 7 && strings.EqualFold(path[:len(extendedPrefix)], extendedPrefix) &&
		isASCIILetter(path[4]) && path[5] == ':' && path[6] == '\\' {
		return path[len(extendedPrefix):]
	}
	return path
}

func isASCIILetter(ch byte) bool {
	return ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func classifyLaunchLocation(path string) (launchLocation, error) {
	return classifyLaunchLocationWith(path, windows.GetDriveType)
}

func classifyLaunchLocationWith(path string, getDriveType func(*uint16) uint32) (launchLocation, error) {
	volume := filepath.VolumeName(filepath.Clean(path))
	if volume == "" {
		return "", fmt.Errorf("determine executable volume: path has no volume")
	}
	if strings.HasPrefix(volume, `\\`) {
		return launchLocationUNC, nil
	}
	root := volume
	if !strings.HasSuffix(root, `\`) {
		root += `\`
	}
	rootPtr, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", fmt.Errorf("determine executable volume: %w", err)
	}
	switch driveType := getDriveType(rootPtr); driveType {
	case windows.DRIVE_REMOTE:
		return launchLocationRemoteDrive, nil
	case windows.DRIVE_UNKNOWN, windows.DRIVE_NO_ROOT_DIR:
		return "", fmt.Errorf("determine executable volume: Windows returned drive type %d", driveType)
	default:
		return launchLocationLocal, nil
	}
}
