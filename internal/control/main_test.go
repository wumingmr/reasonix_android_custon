package control

import (
	"fmt"
	"os"
	"testing"

	"reasonix/internal/testenv"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	cleanupUserState, err := testenv.IsolateUserState()
	if err != nil {
		panic(err)
	}
	if os.Getenv("REASONIX_CREDENTIALS_STORE") == "" {
		_ = os.Setenv("REASONIX_CREDENTIALS_STORE", "file")
	}
	goleak.VerifyTestMain(m, goleak.Cleanup(func(exitCode int) {
		// A controller test that never closes its session service keeps the
		// writer lease, which only Windows reports as a t.TempDir failure.
		if leak := testenv.VerifyNoLeakedFileLocks(); leak != nil {
			fmt.Fprintln(os.Stderr, leak)
			if exitCode == 0 {
				exitCode = 1
			}
		}
		cleanupUserState()
		os.Exit(exitCode)
	}))
}
