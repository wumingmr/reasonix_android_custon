package pathidentity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRejectsEmptyAndUnboundRelativePaths(t *testing.T) {
	for _, path := range []string{"", "  ", "relative"} {
		_, err := Resolve(path, Options{FollowLeaf: true})
		var identityErr *Error
		if !errors.As(err, &identityErr) || identityErr.Kind != ErrorInvalid {
			t.Fatalf("Resolve(%q) error = %v", path, err)
		}
	}
}

func TestResolveMissingTailThroughSymlink(t *testing.T) {
	realParent := t.TempDir()
	aliasParent := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	realPhysical, err := filepath.EvalSymlinks(realParent)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(realPhysical, "missing", "leaf")
	got, err := Resolve(filepath.Join(aliasParent, "missing", "leaf"), Options{FollowLeaf: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.PhysicalPath != want {
		t.Fatalf("PhysicalPath = %q, want %q", got.PhysicalPath, want)
	}
}

func TestResolveCanPreserveLeafSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	followed, err := Resolve(link, Options{FollowLeaf: true})
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := Resolve(link, Options{FollowLeaf: false})
	if err != nil {
		t.Fatal(err)
	}
	physicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if followed.PhysicalPath != filepath.Join(physicalDir, "target") || preserved.PhysicalPath != filepath.Join(physicalDir, "link") {
		t.Fatalf("followed=%q preserved=%q", followed.PhysicalPath, preserved.PhysicalPath)
	}
}

func TestSameUsesPhysicalIdentity(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	same, err := Same(dir, alias, Options{FollowLeaf: true})
	if err != nil || !same {
		t.Fatalf("Same = %v, %v", same, err)
	}
}

func TestResolveIdentityIsStableAcrossMissingTailCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "workspace")
	before, err := Resolve(path, Options{FollowLeaf: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := Resolve(path, Options{FollowLeaf: true})
	if err != nil {
		t.Fatal(err)
	}
	if before.Key != after.Key {
		t.Fatalf("identity changed after creation: %q != %q", before.Key, after.Key)
	}
}

func TestResolveReportsLinkLoop(t *testing.T) {
	dir := t.TempDir()
	left, right := filepath.Join(dir, "left"), filepath.Join(dir, "right")
	if err := os.Symlink(right, left); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(left, right); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(left, Options{FollowLeaf: true})
	var identityErr *Error
	if !errors.As(err, &identityErr) || identityErr.Kind != ErrorLinkLoop {
		t.Fatalf("error = %v", err)
	}
}
