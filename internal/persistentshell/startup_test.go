package persistentshell

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
)

func TestFailedPTYStartupRetainsDiagnosticsAndDoesNotRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX fixture to simulate runtime initialization failure")
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "startup.sh")
	if err := os.WriteFile(fixture, []byte("echo attempt >> attempts\necho '*** fatal error - CreateFileMapping S-1-5-21-1.1, Win32 error 5. Terminating.' >&2\nsleep 0.1\nexit 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := testManager(t)
	req := Request{Argv: []string{"/bin/sh", fixture}, Dir: dir, Env: os.Environ(), Shell: sandbox.Shell{Kind: sandbox.ShellSh, Path: "/bin/sh"}, Command: "touch user-command-ran", Timeout: time.Second}
	for range 3 {
		res := m.Run(context.Background(), req)
		if res.Started || res.Err == nil || res.FailurePhase != tool.ShellPhaseLaunch || !strings.Contains(res.Output, "CreateFileMapping") {
			t.Fatalf("lost startup evidence: %+v", res)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "attempts"))
	if err != nil || strings.Count(string(data), "attempt") != 1 {
		t.Fatalf("repeated startup: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "user-command-ran")); !os.IsNotExist(err) {
		t.Fatal("user command was run")
	}
	m.Rotate()
	m.Run(context.Background(), req)
	data, _ = os.ReadFile(filepath.Join(dir, "attempts"))
	if strings.Count(string(data), "attempt") != 2 {
		t.Fatalf("explicit reset did not allow retry: %q", data)
	}
}
