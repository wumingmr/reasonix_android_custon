package session

import (
	"testing"

	"reasonix/internal/testenv"
)

// A Service holds its writer lease until CloseAll or its idle TTL, so a test
// that forgets to close one still owns the lease when t.TempDir removes the
// directory. Only Windows reports that as a cleanup failure; this guard makes
// it deterministic here.
func TestMain(m *testing.M) {
	testenv.RunWithLeaseGuard(m)
}
