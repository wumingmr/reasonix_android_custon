package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"reasonix/internal/pathidentity"
)

// refuseCanonicalizingUnder makes every path below mount fail to canonicalize
// the way a Box Drive subpath does on Windows, while the files stay readable.
func refuseCanonicalizingUnder(t *testing.T, mount string) {
	t.Helper()
	prev := resolvePathIdentity
	t.Cleanup(func() { resolvePathIdentity = prev })
	resolvePathIdentity = func(path string, options pathidentity.Options) (pathidentity.Identity, error) {
		if strings.HasPrefix(path, mount+string(os.PathSeparator)) {
			return pathidentity.Identity{}, &fs.PathError{Op: "GetFinalPathNameByHandle", Path: path, Err: syscall.ENOENT}
		}
		return prev(path, options)
	}
}

func TestLoadForRootReadsProjectConfigUnderUncanonicalizableMount(t *testing.T) {
	isolateUserConfigHome(t)
	mount := t.TempDir()
	root := filepath.Join(mount, "quant_research")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte("[ui]\ncursor_shape = \"block\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refuseCanonicalizingUnder(t, mount)

	cfg, err := LoadForRoot(root)
	if err != nil {
		t.Fatalf("LoadForRoot(%q): %v", root, err)
	}
	if cfg.UI.CursorShape != "block" {
		t.Fatalf("cursor_shape = %q, want the project file's block", cfg.UI.CursorShape)
	}
}

func TestLoadForRootWithoutProjectConfigUnderUncanonicalizableMount(t *testing.T) {
	isolateUserConfigHome(t)
	mount := t.TempDir()
	root := filepath.Join(mount, "Photos")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	refuseCanonicalizingUnder(t, mount)

	if _, err := LoadForRoot(root); err != nil {
		t.Fatalf("LoadForRoot(%q) with no reasonix.toml: %v", root, err)
	}
}

func TestProjectConfigLinkUnderUncanonicalizableMountStillRefused(t *testing.T) {
	mount := t.TempDir()
	root := filepath.Join(mount, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "real.toml")
	if err := os.WriteFile(target, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "reasonix.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	refuseCanonicalizingUnder(t, mount)

	if got, err := resolveConfigAccessPath(link, false); err == nil {
		t.Fatalf("resolveConfigAccessPath(link) = %q, want the unresolvable link refused", got)
	}
}
