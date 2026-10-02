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
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	m := New()
	m.Retain()
	t.Cleanup(m.Release)
	return m
}

func posixShell(t *testing.T) sandbox.Shell {
	t.Helper()
	sh := sandbox.ResolveShell("bash", "", nil)
	if !sh.Kind.IsPOSIX() {
		t.Skip("POSIX shell required")
	}
	return sh
}

func runPersistent(t *testing.T, m *Manager, sh sandbox.Shell, dir, command string, timeout time.Duration) Result {
	t.Helper()
	ctx := context.Background()
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	return m.Run(ctx, Request{
		Argv: InteractiveArgv(sh),
		Dir:  dir,
		Env: []string{
			"PATH=" + path,
			"HOME=" + dir,
			"TERM=dumb",
			"NO_COLOR=1",
			"PAGER=cat",
			"GIT_PAGER=cat",
			"BASH_SILENCE_DEPRECATION_WARNING=1",
		},
		Command: command,
		Timeout: timeout,
		Shell:   sh,
	})
}

func TestPersistentShellKeepsWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("covered by the PowerShell persistence test on Windows")
	}
	sh := posixShell(t)
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	m := testManager(t)
	if res := runPersistent(t, m, sh, dir, "cd sub", 5*time.Second); res.Err != nil {
		t.Fatalf("cd: %v (%q)", res.Err, res.Output)
	}
	res := runPersistent(t, m, sh, dir, "pwd", 5*time.Second)
	if res.Err != nil {
		t.Fatalf("pwd: %v (%q)", res.Err, res.Output)
	}
	want, err := filepath.EvalSymlinks(sub)
	if err != nil {
		want = sub
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(res.Output))
	if err != nil {
		got = strings.TrimSpace(res.Output)
	}
	if got != want && !strings.Contains(res.Output, filepath.Base(sub)) {
		t.Fatalf("pwd output %q, want %q", res.Output, want)
	}
}

func TestPersistentShellKeepsExportedEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("covered by the PowerShell persistence test on Windows")
	}
	sh := posixShell(t)
	dir := t.TempDir()
	m := testManager(t)
	if res := runPersistent(t, m, sh, dir, "export REASONIX_PERSIST=ok", 5*time.Second); res.Err != nil {
		t.Fatalf("export: %v (%q)", res.Err, res.Output)
	}
	res := runPersistent(t, m, sh, dir, "printf '%s\\n' \"$REASONIX_PERSIST\"", 5*time.Second)
	if res.Err != nil {
		t.Fatalf("printf: %v (%q)", res.Err, res.Output)
	}
	if !strings.Contains(res.Output, "ok") {
		t.Fatalf("env output %q", res.Output)
	}
}

func TestPersistentShellTimeoutResetsState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX timeout reset")
	}
	sh := posixShell(t)
	dir := t.TempDir()
	m := testManager(t)
	if res := runPersistent(t, m, sh, dir, "cd /", 5*time.Second); res.Err != nil {
		t.Fatalf("cd: %v", res.Err)
	}
	res := runPersistent(t, m, sh, dir, "sleep 30", 200*time.Millisecond)
	if !res.TimedOut {
		t.Fatalf("want timeout, got %+v", res)
	}
	pwd := runPersistent(t, m, sh, dir, "pwd", 5*time.Second)
	if pwd.Err != nil {
		t.Fatalf("pwd after timeout: %v (%q)", pwd.Err, pwd.Output)
	}
	if strings.TrimSpace(pwd.Output) == "/" {
		t.Fatalf("timeout must reset cwd, still at /: %q", pwd.Output)
	}
	if !strings.Contains(pwd.Output, dir) {
		t.Fatalf("pwd after timeout = %q, want workspace %q", pwd.Output, dir)
	}
}

func TestPersistentShellMissingPowerShellDoesNotStartCommand(t *testing.T) {
	sh := sandbox.Shell{Kind: sandbox.ShellPowerShell, Path: filepath.Join(t.TempDir(), "missing-powershell")}
	if !Supports(sh) {
		t.Fatal("PowerShell must support the framed session protocol")
	}
	m := testManager(t)
	res := m.Run(context.Background(), Request{
		Argv:    InteractiveArgv(sh),
		Command: "Get-Location",
		Shell:   sh,
	})
	if res.Started {
		t.Fatalf("a PowerShell request must not start a shell: %+v", res)
	}
	if res.Err == nil || res.ExitCodeKnown {
		t.Fatalf("missing executable must have an unknown exit status: %+v", res)
	}
}

func TestPersistentShellNonzeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX nonzero exit")
	}
	sh := posixShell(t)
	m := testManager(t)
	res := runPersistent(t, m, sh, t.TempDir(), "(exit 7)", 5*time.Second)
	if res.ExitCode != 7 {
		t.Fatalf("exit code=%d err=%v out=%q", res.ExitCode, res.Err, res.Output)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "exit status 7") {
		t.Fatalf("err=%v", res.Err)
	}
}
