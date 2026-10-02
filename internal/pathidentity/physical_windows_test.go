//go:build windows

package pathidentity

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func junctionFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	target, alias := filepath.Join(root, "target"), filepath.Join(root, "alias")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", alias, target).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, out)
	}
	return alias, target
}

func TestResolveWindowsJunctionEntryChildrenAndMissingTail(t *testing.T) {
	alias, target := junctionFixture(t)
	if err := os.Mkdir(filepath.Join(target, "locks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "locks", "existing.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tail := range []string{"", "locks", `locks\existing.lock`, `locks\absent\new.lock`} {
		t.Run(tail, func(t *testing.T) {
			left, err := Resolve(filepath.Join(alias, tail), Options{FollowLeaf: true})
			if err != nil {
				t.Fatal(err)
			}
			right, err := Resolve(filepath.Join(target, tail), Options{FollowLeaf: true})
			if err != nil || left.Key != right.Key || left.PhysicalPath != right.PhysicalPath {
				t.Fatalf("alias=%+v target=%+v err=%v", left, right, err)
			}
			if left.AccessPath != filepath.Join(alias, tail) {
				t.Fatal("access spelling changed")
			}
		})
	}
	if _, err := os.Lstat(filepath.Join(target, "locks", "absent")); !os.IsNotExist(err) {
		t.Fatalf("missing tail was created: %v", err)
	}
}

func TestResolveWindowsJunctionPreservesOnlyLeaf(t *testing.T) {
	alias, target := junctionFixture(t)
	entry, err := Resolve(alias, Options{FollowLeaf: false})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := Resolve(filepath.Dir(alias), Options{FollowLeaf: true})
	if err != nil || entry.PhysicalPath != filepath.Join(parent.PhysicalPath, filepath.Base(alias)) {
		t.Fatalf("leaf entry followed: %+v, %v", entry, err)
	}
	child, err := Resolve(filepath.Join(alias, "child"), Options{FollowLeaf: false})
	real, realErr := Resolve(filepath.Join(target, "child"), Options{FollowLeaf: false})
	if err != nil || realErr != nil || child.Key != real.Key {
		t.Fatalf("parent junction not resolved: %+v / %+v, %v / %v", child, real, err, realErr)
	}
}

func TestResolveWindowsCrossVolumeJunction(t *testing.T) {
	root := t.TempDir()
	// GitHub's Windows runners place RUNNER_TEMP on the work drive and the
	// process temp directory on the system drive. Local single-drive machines
	// still execute the unconditional junction cases above.
	other := os.Getenv("RUNNER_TEMP")
	if !filepath.IsAbs(other) || strings.EqualFold(filepath.VolumeName(root), filepath.VolumeName(other)) {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("Windows CI must provide temporary directories on two volumes")
		}
		t.Skip("no temporary directory on a second volume")
	}
	target, err := os.MkdirTemp(other, "reasonix-junction-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(target) })
	alias := filepath.Join(root, "alias")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", alias, target).CombinedOutput(); err != nil {
		t.Fatalf("create cross-volume junction: %v: %s", err, out)
	}
	for _, tail := range []string{"", `missing\new.lock`} {
		left, err := Resolve(filepath.Join(alias, tail), Options{FollowLeaf: true})
		right, rightErr := Resolve(filepath.Join(target, tail), Options{FollowLeaf: true})
		if err != nil || rightErr != nil || left.Key != right.Key || left.PhysicalPath != right.PhysicalPath {
			t.Fatalf("cross-volume identity split: %+v / %+v, %v / %v", left, right, err, rightErr)
		}
	}
}

func TestResolveWindowsRejectsDanglingJunction(t *testing.T) {
	alias, target := junctionFixture(t)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{alias, filepath.Join(alias, "missing.lock")} {
		_, err := Resolve(path, Options{FollowLeaf: true})
		var failure *Error
		if !errors.As(err, &failure) || failure.Stage != "physical" || failure.Kind != ErrorUnavailable {
			t.Fatalf("dangling junction accepted: %q, %v", path, err)
		}
	}
}

func TestResolveWindowsLongPath(t *testing.T) {
	path := t.TempDir()
	for len(path) < 300 {
		path = filepath.Join(path, strings.Repeat("a", 40))
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	plain, err := Resolve(path, Options{FollowLeaf: true})
	extended, extendedErr := Resolve(extendedWindowsPath(path), Options{FollowLeaf: true})
	if err != nil || extendedErr != nil || plain.Key != extended.Key {
		t.Fatalf("long path identity: %+v / %+v, %v / %v", plain, extended, err, extendedErr)
	}
}

func TestWindowsNativePathSpelling(t *testing.T) {
	for _, pair := range [][2]string{
		{`C:\dir`, `\\?\C:\dir`},
		{`\\server\share\dir`, `\\?\UNC\server\share\dir`},
		{`\\?\Volume{test}\dir`, `\\?\Volume{test}\dir`},
	} {
		if got := extendedWindowsPath(pair[0]); got != pair[1] {
			t.Errorf("extend %q = %q", pair[0], got)
		}
		if got, want := ntPhysicalPath(pair[0]), `\??\`+pair[1][4:]; got != want {
			t.Errorf("NT path %q = %q, want %q", pair[0], got, want)
		}
		if got := stripExtendedPrefix(pair[1]); got != pair[0] {
			t.Errorf("strip %q = %q", pair[1], got)
		}
	}
}
