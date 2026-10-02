//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNativeJunctionUsesTargetIdentity(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	alias := filepath.Join(dir, "alias")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", alias, target).CombinedOutput(); err != nil {
		t.Fatalf("create junction fixture: %v: %s", err, out)
	}
	left, right := nativePath(alias), nativePath(target)
	if !left.OK || !right.OK || left.FileID == "" || left.Volume == "" ||
		left.FileID != right.FileID || left.Volume != right.Volume || left.Path != right.Path {
		t.Fatalf("junction identity mismatch: %+v / %+v", left, right)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if got := nativePath(alias); got.OK || len(got.Errors) == 0 {
		t.Fatalf("broken junction must retain failure: %+v", got)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("native probe recreated target: %v", err)
	}
}

func TestNormalizeNativePath(t *testing.T) {
	for input, want := range map[string]string{
		`\\?\C:\Users\example`: `C:\Users\example`,
		`\\?\UNC\server\share`: `\\server\share`,
		`\\?\Volume{test}\dir`: `\\?\Volume{test}\dir`,
		`C:\normal`:            `C:\normal`,
	} {
		if got := normalizeNativePath(input); got != want {
			t.Errorf("normalize %q = %q, want %q", input, got, want)
		}
	}
}
