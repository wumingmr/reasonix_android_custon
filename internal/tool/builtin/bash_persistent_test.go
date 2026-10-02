package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"reasonix/internal/persistentshell"
	"reasonix/internal/sandbox"
)

func TestPowerShellNeverUsesPersistentSession(t *testing.T) {
	b := bash{persistent: persistentshell.New()}
	sh := sandbox.Shell{Kind: sandbox.ShellPowerShell, Path: "pwsh"}
	if b.shouldUsePersistent(context.Background(), bashParams{}, sh) {
		t.Fatal("ordinary PowerShell calls must use one-shot processes")
	}
}

func persistentBash(t *testing.T, workDir string) bash {
	t.Helper()
	m := persistentshell.New()
	m.Retain()
	t.Cleanup(m.Release)
	sh := sandbox.ResolveShell("", "", nil)
	return bash{
		sb:         sandbox.Spec{Mode: "off"},
		shell:      sh,
		workDir:    workDir,
		timeout:    8 * time.Second,
		persistent: m,
	}
}

func TestBashPersistentKeepsWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX persistent bash")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b := persistentBash(t, dir)
	ctx := fullAccessBashTestContext(t.Context())
	if _, err := b.Execute(ctx, argsJSON(t, map[string]any{"command": "cd sub"})); err != nil {
		t.Fatalf("cd: %v", err)
	}
	out, err := b.Execute(ctx, argsJSON(t, map[string]any{"command": "pwd"}))
	if err != nil {
		t.Fatalf("pwd: %v (%q)", err, out)
	}
	want, _ := filepath.EvalSymlinks(sub)
	got := strings.TrimSpace(out)
	if resolved, rerr := filepath.EvalSymlinks(got); rerr == nil {
		got = resolved
	}
	if got != want && !strings.Contains(out, "sub") {
		t.Fatalf("pwd=%q want %q", out, want)
	}
}

func TestBashWithoutPersistentDoesNotKeepCwd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX one-shot bash")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b := bash{
		sb:      sandbox.Spec{Mode: "off"},
		shell:   sandbox.ResolveShell("", "", nil),
		workDir: dir,
		timeout: 8 * time.Second,
	}
	ctx := fullAccessBashTestContext(t.Context())
	if _, err := b.Execute(ctx, argsJSON(t, map[string]any{"command": "cd sub"})); err != nil {
		t.Fatalf("cd: %v", err)
	}
	out, err := b.Execute(ctx, argsJSON(t, map[string]any{"command": "pwd"}))
	if err != nil {
		t.Fatalf("pwd: %v (%q)", err, out)
	}
	got := strings.TrimSpace(out)
	if resolved, rerr := filepath.EvalSymlinks(got); rerr == nil {
		got = resolved
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Fatalf("one-shot pwd=%q want workspace %q", out, want)
	}
}

func TestBashPersistentSchemaUnchanged(t *testing.T) {
	plain := bash{}.Schema()
	withPTY := bash{persistent: persistentshell.New()}.Schema()
	if string(plain) != string(withPTY) {
		t.Fatalf("persistent PTY must not change bash schema\nplain=%s\nwith=%s", plain, withPTY)
	}
	if (bash{}).Description() != (bash{persistent: persistentshell.New()}).Description() {
		t.Fatal("persistent PTY must not change bash description")
	}
	var schema map[string]any
	if err := json.Unmarshal(plain, &schema); err != nil {
		t.Fatal(err)
	}
}

func TestBashPersistentUnicodeWithoutUTF8Locale(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX persistent bash")
	}
	t.Setenv("LC_ALL", "C")
	t.Setenv("INPUTRC", "/dev/null")
	b := persistentBash(t, t.TempDir())
	b.shell = sandbox.ResolveShell("bash", "", nil)
	ctx := fullAccessBashTestContext(t.Context())
	for _, command := range []string{
		"export RX_UNICODE='中文😀'; printf '%s' \"$RX_UNICODE\"",
		"printf '%s' \"$RX_UNICODE\"",
	} {
		out, err := b.Execute(ctx, argsJSON(t, map[string]any{"command": command}))
		if err != nil || strings.TrimSpace(out) != "中文😀" {
			t.Fatalf("Unicode command: output=%q err=%v", out, err)
		}
	}
}

func TestBashPersistentSkipsBackgroundAndWriteEscalation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX persistent bash")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	extra := t.TempDir()
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b := persistentBash(t, dir)
	ctx := fullAccessBashTestContext(t.Context())
	if _, err := b.Execute(ctx, argsJSON(t, map[string]any{"command": "cd sub"})); err != nil {
		t.Fatalf("cd: %v", err)
	}
	out, err := b.Execute(ctx, argsJSON(t, map[string]any{
		"command":               "pwd",
		"additional_write_dirs": []string{extra},
		"justification":         "test one-shot fallback",
	}))
	if err != nil {
		t.Fatalf("escalated pwd: %v (%q)", err, out)
	}
	got := strings.TrimSpace(out)
	if resolved, rerr := filepath.EvalSymlinks(got); rerr == nil {
		got = resolved
	}
	wantSub, _ := filepath.EvalSymlinks(sub)
	if got == wantSub {
		t.Fatalf("additional_write_dirs must not reuse persistent cwd, got %q", out)
	}
}

// A foreground command that backgrounds a child stays on the one-shot path:
// only there does #3702's process-group reap run, and only there can the
// child's later output not land inside the next command's result.
func TestBashPersistentSkipsBackgroundOperator(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX persistent bash")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b := persistentBash(t, dir)
	ctx := fullAccessBashTestContext(t.Context())
	if _, err := b.Execute(ctx, argsJSON(t, map[string]any{"command": "cd sub"})); err != nil {
		t.Fatalf("cd: %v", err)
	}
	// A backgrounding command must not observe the persistent cwd.
	out, err := b.Execute(ctx, argsJSON(t, map[string]any{"command": "(sleep 0) & wait; pwd"}))
	if err != nil {
		t.Fatalf("background pwd: %v (%q)", err, out)
	}
	got := strings.TrimSpace(out)
	if resolved, rerr := filepath.EvalSymlinks(got); rerr == nil {
		got = resolved
	}
	wantSub, _ := filepath.EvalSymlinks(sub)
	if got == wantSub {
		t.Fatalf("a backgrounding command must run one-shot, got %q", out)
	}
}

func TestHasBackgroundStatement(t *testing.T) {
	cases := map[string]bool{
		"sleep 1 &":                  true,
		"npm run dev &":              true,
		"(while true; do :; done) &": true,
		"sleep 1":                    false,
		"echo 'a & b'":               false,
		"grep -n 'x && y' file":      false,
		"a && b":                     false,
	}
	for command, want := range cases {
		if got := hasBackgroundStatement(command); got != want {
			t.Fatalf("hasBackgroundStatement(%q)=%v want %v", command, got, want)
		}
	}
}

// The persistent launch must carry the session-private temporary directory into
// the sandbox profile, not only into the child environment: the same spec sets
// TMPDIR/GOCACHE, so a profile without that directory denies every write
// through them.
func TestBashPersistentLaunchCarriesSessionTemp(t *testing.T) {
	sessionTemp := t.TempDir()
	spec := sandbox.Spec{Mode: "enforce", WriteRoots: []string{t.TempDir()}}
	launch := sandbox.PrepareArgs(spec, persistentshell.InteractiveArgv(sandbox.ResolveShell("", "", nil)), sessionTemp)
	if launch.SessionTemp != sessionTemp {
		t.Fatalf("session temp %q not carried into the launch", launch.SessionTemp)
	}
	if len(launch.EnvOverrides) == 0 {
		t.Fatal("session temp env overrides missing")
	}
	if !launch.Wrapped {
		t.Skip("no OS sandbox backend on this host")
	}
	if !strings.Contains(strings.Join(launch.Argv, " "), sessionTemp) {
		t.Fatalf("sandbox argv does not reference the session temp dir: %v", launch.Argv)
	}
}
