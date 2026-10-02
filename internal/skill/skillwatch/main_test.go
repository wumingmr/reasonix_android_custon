package skillwatch

import (
	"os"
	"testing"
)

// The helper backend re-executes os.Executable(), which is this test binary.
// Without this entry the child re-runs the suite instead of serving the pipe,
// so every registration times out and the service degrades to scan fallback.
func TestMain(m *testing.M) {
	if MaybeRunHelper() {
		return
	}
	os.Exit(m.Run())
}
