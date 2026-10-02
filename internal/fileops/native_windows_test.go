//go:build windows

package fileops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsSnapshotUsesVolumeFileIndexAndChangeTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version.txt")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	target, before := DiskSnapshot(path, info)
	if !strings.Contains(target.Key, "volume=") || !strings.Contains(target.Key, "fileindex=") {
		t.Fatalf("target lacks native identity: %q", target.Key)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES|windows.FILE_WRITE_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	var basic windowsFileBasicInfo
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err != nil {
		t.Fatal(err)
	}
	// Two writes can share a filesystem timestamp tick. Set a distinct native
	// ChangeTime explicitly so this checks the version inputs, not clock speed.
	change := windowsFileBasicInfo{ChangeTime: basic.ChangeTime + 20_000_000}
	if err := windows.SetFileInformationByHandle(handle, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&change)), uint32(unsafe.Sizeof(change))); err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	afterTarget, after := DiskSnapshot(path, afterInfo)
	if target != afterTarget || info.Size() != afterInfo.Size() || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("ChangeTime fixture unexpectedly changed identity, size or mtime")
	}
	if before == after {
		t.Fatal("native ChangeTime was omitted from the version")
	}
}

func TestWindowsSnapshotDetectsSameSizeReplacementWithRestoredMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replacement.txt")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	target, before := DiskSnapshot(path, info)
	// Retain the old file so its native identity cannot be recycled for the new one.
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	afterTarget, after := DiskSnapshot(path, afterInfo)
	if info.Size() != afterInfo.Size() || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("replacement fixture must preserve size and mtime")
	}
	if target.Key == afterTarget.Key || before == after {
		t.Fatal("replacement did not change native identity and version")
	}
}

func TestWindowsHandleSnapshotUsesTheSourceHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handle-version.txt")
	if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	target, version := DiskHandleSnapshot(path, file, info)
	if !strings.Contains(target.Key, "volume=") || !strings.Contains(target.Key, "fileindex=") {
		t.Fatalf("handle target lacks native identity: %q", target.Key)
	}
	if version == "" {
		t.Fatal("handle snapshot returned an empty version")
	}
}

func TestWindowsSnapshotDetectsSecurityDescriptorChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acl-version.txt")
	if err := os.WriteFile(path, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	_, before := DiskSnapshot(path, info)

	// os.Chmod does not represent NTFS ACL changes. Disabling inheritance
	// changes the real security descriptor while preserving file contents.
	cmd := exec.Command("icacls.exe", path, "/inheritance:d")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("change ACL inheritance: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("icacls.exe", path, "/inheritance:e").Run() })
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	_, after := DiskSnapshot(path, afterInfo)
	if before == after {
		t.Fatal("security descriptor change did not advance the native version")
	}
}
