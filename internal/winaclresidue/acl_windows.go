//go:build windows

package winaclresidue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"reasonix/internal/proc"
)

const (
	allApplicationPackagesSID           = "S-1-15-2-1"
	allRestrictedApplicationPackagesSID = "S-1-15-2-2"
	// stillActiveExitCode is STILL_ACTIVE from GetExitCodeProcess.
	stillActiveExitCode = 259
	// icacls can stall on antivirus-scanned volumes; the bound keeps a stuck
	// cleanup from pinning startup or a credential read.
	icaclsTimeout = 30 * time.Second
)

// markerDir is the directory the retired backend used for crash-residue
// markers; the name is part of the on-disk contract with older builds.
func markerDir() string {
	return filepath.Join(os.TempDir(), "windows-sandbox-denylocks")
}

// SweepStaleMarkers removes the ACEs recorded by crashed runs of the retired
// backend whose owning process is provably gone, then deletes their markers.
// It is best-effort: errors keep the marker for a later sweep and never block
// the caller.
func SweepStaleMarkers() {
	dir := markerDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	sids := residueSIDs()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		pid, ok := markerOwnerPID(entry.Name())
		if !ok || !processExited(pid) {
			continue
		}
		sweepMarkerFile(filepath.Join(dir, entry.Name()), sids)
	}
}

// residueSIDs is the exact trustee set the retired backend applied; removing
// only these cannot disturb a legitimate ACL.
func residueSIDs() []string {
	userSID, _ := currentProcessUserSIDString()
	return dedupeSIDStrings([]string{allApplicationPackagesSID, allRestrictedApplicationPackagesSID, userSID})
}

// sweepMarkerFile removes the recorded ACEs and deletes the marker only when
// every removal succeeded, so a failed cleanup keeps its evidence for the next
// sweep instead of orphaning the ACE on disk.
func sweepMarkerFile(markerPath string, sids []string) {
	clean := true
	for _, e := range readResidueMarker(markerPath) {
		if isWindowsSystemRoot(e.path) {
			continue
		}
		if _, err := os.Stat(e.path); err != nil {
			continue
		}
		flag := "/remove:g"
		if e.kind == residueDeny {
			flag = "/remove:d"
		}
		for _, sid := range sids {
			if err := icacls(e.path, flag, "*"+sid, "/C"); err != nil {
				clean = false
			}
		}
	}
	if clean {
		_ = os.Remove(markerPath)
	}
}

// processExited reports whether a marker owner is provably gone. This package
// writes no markers, so one carrying our own PID was left by a dead
// predecessor after PID reuse. Access denied does not prove death.
func processExited(pidText string) bool {
	if pidText == strconv.Itoa(os.Getpid()) {
		return true
	}
	pid, err := strconv.ParseUint(pidText, 10, 32)
	if err != nil || pid == 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(handle)
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code != stillActiveExitCode
}

func currentProcessUserSIDString() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	if user == nil || user.User.Sid == nil {
		return "", fmt.Errorf("current process token has no user SID")
	}
	return user.User.Sid.String(), nil
}

// systemRootTool resolves a Windows system tool below %SystemRoot%\System32
// so a PATH-shadowed binary can never run in its place.
func systemRootTool(name string) string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = os.Getenv("windir")
	}
	if root == "" {
		root = `C:\Windows`
	}
	full := filepath.Join(root, "System32", name)
	if _, err := os.Stat(full); err == nil {
		return full
	}
	return name
}

func icacls(path string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), icaclsTimeout)
	defer cancel()
	cmd := proc.CommandContext(ctx, systemRootTool("icacls.exe"), append([]string{path}, args...)...)
	proc.HideWindow(cmd)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("icacls %q %s: %w: %s", path, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}
