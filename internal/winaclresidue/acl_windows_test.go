//go:build windows

package winaclresidue

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A dead run's marker is swept (deny removed, marker deleted); a marker whose
// owner is still alive is left for that owner.
func TestSweepStaleMarkersRemovesDeadRunResidueOnly(t *testing.T) {
	t.Setenv("TEMP", t.TempDir())
	userSID, err := currentProcessUserSIDString()
	if err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(t.TempDir(), "dead.txt")
	live := filepath.Join(t.TempDir(), "live.txt")
	for _, path := range []string{dead, live} {
		if err := os.WriteFile(path, []byte("secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		installLegacyDeny(t, path, userSID)
		if _, err := os.ReadFile(path); err == nil {
			t.Fatalf("deny did not block reads of %s", path)
		}
	}
	exited := exec.Command("cmd", "/c", "exit 0")
	if err := exited.Run(); err != nil {
		t.Fatal(err)
	}
	deadMarker := writeMarker(t, exited.ProcessState.Pid(), dead)
	liveMarker := writeMarker(t, os.Getppid(), live)

	SweepStaleMarkers()

	if _, err := os.ReadFile(dead); err != nil {
		t.Fatalf("dead run's deny survived the sweep: %v", err)
	}
	if _, err := os.Stat(deadMarker); !os.IsNotExist(err) {
		t.Fatalf("dead marker survived the sweep: %v", err)
	}
	if _, err := os.ReadFile(live); err == nil {
		t.Fatal("live owner's deny was removed")
	}
	if _, err := os.Stat(liveMarker); err != nil {
		t.Fatalf("live marker was deleted: %v", err)
	}
}
