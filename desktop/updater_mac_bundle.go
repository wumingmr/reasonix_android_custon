//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func currentMacAppBundle() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("update: locate current executable: %w", err)
	}
	return macAppBundleForExecutable(exe)
}

func macAppBundleForExecutable(exe string) (string, error) {
	exe = strings.TrimSpace(exe)
	if exe == "" {
		return "", fmt.Errorf("update: current executable path is empty")
	}
	absolute, err := filepath.Abs(exe)
	if err != nil {
		return "", fmt.Errorf("update: make current executable path absolute: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		return "", fmt.Errorf("update: resolve current executable: %w", err)
	}
	// Keep the installed path spelling while requiring the link chain to resolve.
	// An external launcher may instead use its resolved in-bundle target.
	contents, ok := macAppContentsForExecutable(absolute)
	if !ok {
		contents, ok = macAppContentsForExecutable(resolved)
	}
	if !ok {
		return "", fmt.Errorf("update: current executable is not inside a macOS .app bundle")
	}
	app := filepath.Dir(contents)
	info := filepath.Join(contents, "Info.plist")
	if st, err := os.Lstat(info); err != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
		if err == nil {
			err = fmt.Errorf("Info.plist is not a regular file")
		}
		return "", fmt.Errorf("update: current app bundle is invalid: %w", err)
	}
	out, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :CFBundleIdentifier", info).Output()
	if err != nil {
		return "", fmt.Errorf("update: current app bundle is invalid: read bundle identifier: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != macBundleID {
		return "", fmt.Errorf("update: current app bundle identifier %q does not match %q", got, macBundleID)
	}
	return app, nil
}
