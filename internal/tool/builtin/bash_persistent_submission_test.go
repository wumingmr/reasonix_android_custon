package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
)

func TestBashPersistentStagingFailureDoesNotRetryOneShot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX PTY transport fixture")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "shell.sh")
	fixture := "#!/bin/sh\nprintf 'attempt\\n' >> attempts\nIFS= read -r setup\nprintf 'REASONIX_SHELL_READY\\n'\nIFS= read -r block\nexit 0\n"
	if err := os.WriteFile(path, []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	b := persistentBash(t, dir)
	b.shell = sandbox.Shell{Kind: sandbox.ShellBash, Path: path}
	res, err := b.ExecuteDetailed(fullAccessBashTestContext(t.Context()), argsJSON(t, map[string]any{
		"command": "printf '%s' '" + strings.Repeat("x", 4096) + "'",
	}))
	if err == nil || res.Execution == nil || res.Execution.MutationRisk != tool.ShellMutationNotStarted {
		t.Fatalf("staging failure lost not-run semantics: %+v err=%v", res, err)
	}
	attempts, err := os.ReadFile(filepath.Join(dir, "attempts"))
	if err != nil || strings.Count(string(attempts), "attempt") != 1 {
		t.Fatalf("retried failed staging: %q err=%v", attempts, err)
	}
}
