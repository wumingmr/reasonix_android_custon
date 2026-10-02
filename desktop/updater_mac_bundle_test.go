//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMacBundleFixture(t *testing.T, root, identifier string) string {
	t.Helper()
	contents := filepath.Join(root, "Reasonix.app", "Contents")
	if err := os.MkdirAll(filepath.Join(contents, "Resources", "service"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>` + identifier + `</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	return contents
}

func TestMacAppBundleForExecutableSupportsPackagedLayouts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "parent.app cache", "中文目录")
	contents := writeMacBundleFixture(t, root, macBundleID)
	for _, relative := range []string{"MacOS/reasonix-desktop", "Resources/service/reasonix-desktop"} {
		exe := filepath.Join(contents, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(exe, []byte("service"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := macAppBundleForExecutable(exe)
		if err != nil || got != filepath.Dir(contents) {
			t.Fatalf("%s: bundle=%q err=%v", relative, got, err)
		}
	}
}

func TestMacAppBundleForExecutableResolvesCompatibilitySymlink(t *testing.T) {
	contents := writeMacBundleFixture(t, t.TempDir(), macBundleID)
	target := filepath.Join(contents, "Resources", "service", "reasonix-desktop")
	if err := os.WriteFile(target, []byte("service"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(contents, "MacOS", "reasonix-desktop")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "Resources", "service", "reasonix-desktop"), link); err != nil {
		t.Fatal(err)
	}
	if got, err := macAppBundleForExecutable(link); err != nil || got != filepath.Dir(contents) {
		t.Fatalf("bundle=%q err=%v", got, err)
	}
}

func TestMacAppBundleForExecutableRejectsInvalidInputs(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "reasonix-desktop")
	if err := os.WriteFile(outside, []byte("service"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := macAppBundleForExecutable(outside); err == nil || !strings.Contains(err.Error(), "not inside") {
		t.Fatalf("outside error = %v", err)
	}

	contents := writeMacBundleFixture(t, t.TempDir(), "example.invalid")
	exe := filepath.Join(contents, "Resources", "service", "reasonix-desktop")
	if err := os.WriteFile(exe, []byte("service"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := macAppBundleForExecutable(exe); err == nil || !strings.Contains(err.Error(), "identifier") {
		t.Fatalf("identifier error = %v", err)
	}

	malformedContents := writeMacBundleFixture(t, t.TempDir(), macBundleID)
	malformedExe := filepath.Join(malformedContents, "Resources", "service", "reasonix-desktop")
	if err := os.WriteFile(malformedExe, []byte("service"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformedContents, "Info.plist"), []byte("not a plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := macAppBundleForExecutable(malformedExe); err == nil || !strings.Contains(err.Error(), "read bundle identifier") {
		t.Fatalf("malformed plist error = %v", err)
	}

	broken := filepath.Join(contents, "MacOS", "broken")
	if err := os.MkdirAll(filepath.Dir(broken), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", broken); err != nil {
		t.Fatal(err)
	}
	if _, err := macAppBundleForExecutable(broken); err == nil || !strings.Contains(err.Error(), "resolve") {
		t.Fatalf("broken link error = %v", err)
	}
}
