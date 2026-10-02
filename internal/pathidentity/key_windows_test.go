//go:build windows

package pathidentity

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsIdentityKeyFoldsPerDirectory(t *testing.T) {
	caseSensitiveParent := filepath.Clean(`C:\Root`)
	identity := func(path string) string {
		key, err := windowsIdentityKeyBy(path, func(directory string) (bool, bool, error) {
			return !strings.EqualFold(filepath.Clean(directory), caseSensitiveParent), true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	upper := identity(`C:\ROOT\Foo\LEAF.jsonl`)
	lower := identity(`c:\root\foo\leaf.jsonl`)
	if upper == lower {
		t.Fatalf("case-sensitive child names collapsed to %q", upper)
	}
	if got, want := upper, filepath.Clean(`c:\root\Foo\leaf.jsonl`); got != want {
		t.Fatalf("segment-aware identity = %q, want %q", got, want)
	}
}

func TestCaseSensitivityUnsupportedRecognizesWrappedStatus(t *testing.T) {
	for _, status := range []windows.NTStatus{windows.STATUS_INVALID_INFO_CLASS, windows.STATUS_INVALID_PARAMETER, windows.STATUS_NOT_SUPPORTED} {
		if !caseSensitivityQueryUnsupported(status) || !caseSensitivityQueryUnsupported(fmt.Errorf("query: %w", status)) {
			t.Fatalf("unsupported status %v not recognized", status)
		}
	}
	if caseSensitivityQueryUnsupported(nil) || caseSensitivityQueryUnsupported(windows.STATUS_ACCESS_DENIED) {
		t.Fatal("success or access failure treated as unsupported")
	}
}

func TestDirectoryCaseInsensitiveQueriesNativeDirectory(t *testing.T) {
	insensitive, exists, err := directoryCaseInsensitive(t.TempDir())
	if err != nil {
		t.Fatalf("query directory case sensitivity: %v", err)
	}
	if !exists {
		t.Fatal("temporary directory should exist")
	}
	_ = insensitive
}
