package testenv

import (
	"fmt"
	"os"
	"strings"

	"reasonix/internal/filelock"
)

// ReportLeakedFileLocks summarizes leaked locks without failing the binary. Use
// it for packages whose fixtures keep state under a package-scoped scratch home
// that is removed wholesale, where a leak is real debt but does not break the
// per-test t.TempDir cleanup that VerifyNoLeakedFileLocks guards.
func ReportLeakedFileLocks() string {
	held := filelock.HeldPathsForTest()
	if len(held) == 0 {
		return ""
	}
	const shown = 3
	sample := held
	if len(sample) > shown {
		sample = sample[:shown]
	}
	return fmt.Sprintf("note: tests left %d file lock(s) held, for example:\n  %s",
		len(held), strings.Join(sample, "\n  "))
}

// VerifyNoLeakedFileLocks reports the file locks a package's tests left held.
//
// A session.Service keeps its writer lease until CloseAll or its idle TTL, so a
// test that never closes its service still owns the lease when t.TempDir tries
// to remove the directory. POSIX unlinks open files happily, so the leak is
// invisible there; Windows refuses with "The process cannot access the file
// because it is being used by another process" and fails the test during
// cleanup. Calling this from TestMain after m.Run turns that into one
// deterministic failure on every platform, and the reported lock paths contain
// the leaking test's TempDir name.
func VerifyNoLeakedFileLocks() error {
	held := filelock.HeldPathsForTest()
	if len(held) == 0 {
		return nil
	}
	return fmt.Errorf("tests leaked %d file lock(s); close the owning session service (Service.CloseAll) before the test ends:\n  %s",
		len(held), strings.Join(held, "\n  "))
}

// RunWithLeaseGuard runs a package test binary and fails it when a test leaves a
// file lock held. Packages that also need user-state isolation compose this with
// IsolateUserState rather than calling RunWithIsolatedUserState.
func RunWithLeaseGuard(m TestingM) {
	code := m.Run()
	if err := VerifyNoLeakedFileLocks(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
