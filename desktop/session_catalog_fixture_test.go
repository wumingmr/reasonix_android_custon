package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This subprocess deliberately fails admission. Its fixture must still stop
// the resource owner; registering cleanup after the waiter cannot do that.
func TestCatalogFixtureAdmissionFailureProcess(t *testing.T) {
	marker := os.Getenv("REASONIX_TEST_CATALOG_CLEANUP")
	if marker == "" {
		return
	}
	done := make(chan struct{})
	close(done)
	app := &App{}
	app.catalogDone, app.catalogInitialReconcileDone = done, done
	app.catalogCancel = func() {
		if err := os.WriteFile(marker, []byte("owner stopped"), 0600); err != nil {
			t.Error(err)
		}
	}
	waitForInitialCatalogReconcile(t, app) // no published catalog: must fail
}

func TestCatalogFixtureAdmissionFailureStillStopsOwner(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "closed")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCatalogFixtureAdmissionFailureProcess$")
	cmd.Env = append(os.Environ(), "REASONIX_TEST_CATALOG_CLEANUP="+marker)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "session catalog exited before publication") {
		t.Fatalf("expected admission failure, got %v: %s", err, output)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "owner stopped" {
		t.Fatalf("failed admission leaked its owner: %q %v", got, err)
	}
}
