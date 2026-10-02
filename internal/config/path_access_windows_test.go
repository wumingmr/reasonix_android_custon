//go:build windows

package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestConfigJunctionAccessAndLockNames(t *testing.T) {
	root := t.TempDir()
	target, alias := filepath.Join(root, "target"), filepath.Join(root, "alias")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", alias, target).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, out)
	}
	for _, name := range []string{"config.toml", ".env", "reasonix.toml"} {
		for _, existing := range []bool{false, true} {
			physical := filepath.Join(target, name)
			if existing {
				if err := os.WriteFile(physical, []byte("# keep\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, userConfig := range []bool{false, true} {
				left, err := resolveConfigAccessPathUnpinned(filepath.Join(alias, name), userConfig)
				right, rightErr := resolveConfigAccessPathUnpinned(physical, userConfig)
				if err != nil || rightErr != nil || left != right {
					t.Fatalf("config alias resolution: %q / %q, %v / %v", left, right, err, rightErr)
				}
				// Deriving registry names must not create or acquire real user locks.
				leftLock, err := configFileEditLockPathResolved(left)
				rightLock, rightErr := configFileEditLockPathResolved(right)
				if err != nil || rightErr != nil || leftLock != rightLock {
					t.Fatalf("config alias lock names diverge: %v / %v", err, rightErr)
				}
			}
		}
	}
}
