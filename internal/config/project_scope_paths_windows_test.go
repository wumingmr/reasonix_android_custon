//go:build windows

package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Run on Windows; Unix string simulations do not exercise junction identity.
func TestProjectWindowsJunctionAndUNC(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	c := Default()
	if !c.equivalentConfigPath(root, root, filepath.ToSlash(root)) {
		t.Fatal("slash variants differ")
	}
	junction := filepath.Join(root, "external-junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", junction, external).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = os.Remove(junction) })
	if c.authorizedPath(root, []string{root}, filepath.Join(junction, "new")) {
		t.Fatal("junction escaped project grant")
	}
	if !c.authorizedPath(root, []string{external}, filepath.Join(junction, "new")) {
		t.Fatal("external grant did not cover junction target")
	}
	t.Run("UNC", func(t *testing.T) {
		unc := os.Getenv("REASONIX_TEST_UNC_ROOT")
		if unc == "" {
			t.Skip("set REASONIX_TEST_UNC_ROOT to a writable Windows share for native UNC evidence")
		}
		share, err := os.MkdirTemp(unc, "reasonix-diagnostics-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(share) })
		if !c.equivalentConfigPath(root, share, filepath.ToSlash(share)) {
			t.Fatal("UNC separator variants differ")
		}
		if !c.authorizedPath(root, []string{share}, filepath.Join(share, "new")) {
			t.Fatal("UNC descendant rejected")
		}
		if c.authorizedPath(root, []string{share}, share+"-other") {
			t.Fatal("UNC sibling granted")
		}
	})
}
