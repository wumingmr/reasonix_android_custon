package persistentshell

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"reasonix/internal/sandbox"
)

func skipNonPOSIX(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX persistent shell")
	}
}

// A command whose output does not end in a newline used to leave the status
// marker mid-line, which no line-anchored match could find: the command ran to
// its full deadline, returned nothing, and took the session shell with it.
func TestPersistentShellCommandWithoutTrailingNewline(t *testing.T) {
	skipNonPOSIX(t)
	sh := posixShell(t)
	dir := t.TempDir()
	noEOL := filepath.Join(dir, "no-eol.txt")
	if err := os.WriteFile(noEOL, []byte("tail without newline"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ command, want string }{
		{"printf 'hi'", "hi"},
		{"printf 'hi\\n'", "hi\n"},
		{"cat " + noEOL, "tail without newline"},
	}
	m := testManager(t)
	for _, tc := range cases {
		res := runPersistent(t, m, sh, dir, tc.command, 5*time.Second)
		if res.Err != nil {
			t.Fatalf("%s: %v", tc.command, res.Err)
		}
		if res.Output != tc.want {
			t.Fatalf("%s: output=%q want %q", tc.command, res.Output, tc.want)
		}
	}
}

// Output must be byte-identical to what the command wrote. The terminal line
// discipline can emit \r\r\n under pressure, which naive normalisation turned
// into blank lines scattered through model-visible output.
func TestPersistentShellOutputIsByteExact(t *testing.T) {
	skipNonPOSIX(t)
	sh := posixShell(t)
	dir := t.TempDir()
	m := testManager(t)
	for _, n := range []int{100, 20000} {
		var want strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&want, "%d\n", i)
		}
		res := runPersistent(t, m, sh, dir, fmt.Sprintf("seq 1 %d", n), 60*time.Second)
		if res.Err != nil {
			t.Fatalf("seq %d: %v", n, res.Err)
		}
		if res.Output != want.String() {
			t.Fatalf("seq %d: output is %d bytes, want %d", n, len(res.Output), want.Len())
		}
	}
}

// Terminal control sequences never reached the model through the one-shot path
// because it had no tty. A session PTY does, so they are stripped.
func TestPersistentShellStripsTerminalControls(t *testing.T) {
	skipNonPOSIX(t)
	sh := posixShell(t)
	m := testManager(t)
	res := runPersistent(t, m, sh, t.TempDir(), `printf '\033[31mred\033[0m\n'`, 5*time.Second)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Output != "red\n" {
		t.Fatalf("output=%q", res.Output)
	}
}

// A command that reads stdin must fail the way it did under one-shot execution
// instead of blocking the session shell until the deadline.
func TestPersistentShellDetachesStdin(t *testing.T) {
	skipNonPOSIX(t)
	sh := posixShell(t)
	m := testManager(t)
	start := time.Now()
	res := runPersistent(t, m, sh, t.TempDir(), "read -r answer; printf 'got:%s\\n' \"$answer\"", 10*time.Second)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("stdin read blocked for %s", elapsed)
	}
	if res.TimedOut {
		t.Fatal("a command reading stdin must not consume the deadline")
	}
	if !strings.Contains(res.Output, "got:") {
		t.Fatalf("output=%q", res.Output)
	}
}

// A timed-out command still owes the model whatever it printed, and must say
// that shell state is gone.
func TestPersistentShellTimeoutReportsPartialOutputAndReset(t *testing.T) {
	skipNonPOSIX(t)
	sh := posixShell(t)
	m := testManager(t)
	res := runPersistent(t, m, sh, t.TempDir(), "printf 'before\\n'; sleep 30", 500*time.Millisecond)
	if !res.TimedOut {
		t.Fatalf("want timeout, got %+v", res)
	}
	if !res.Reset {
		t.Fatal("a timeout retires the shell and must report it")
	}
	if !strings.Contains(res.Output, "before") {
		t.Fatalf("partial output lost: %q", res.Output)
	}
}

// Cancellation reports what ran and retires the shell.
func TestPersistentShellCancelReportsPartialOutput(t *testing.T) {
	skipNonPOSIX(t)
	sh := posixShell(t)
	m := testManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	res := m.Run(ctx, Request{
		Argv:    InteractiveArgv(sh),
		Dir:     t.TempDir(),
		Env:     []string{"PATH=" + os.Getenv("PATH"), "TERM=dumb"},
		Command: "printf 'started\\n'; sleep 30",
		Timeout: 30 * time.Second,
		Shell:   sh,
	})
	if !res.Canceled {
		t.Fatalf("want cancel, got %+v", res)
	}
	if !res.Reset || !strings.Contains(res.Output, "started") {
		t.Fatalf("reset=%v output=%q", res.Reset, res.Output)
	}
}

// A multi-line command reaches the shell as one physical line, so no PS2 prompt
// or wrapper source can appear in the result.
func TestPersistentShellMultilineCommand(t *testing.T) {
	skipNonPOSIX(t)
	sh := posixShell(t)
	dir := t.TempDir()
	m := testManager(t)
	res := runPersistent(t, m, sh, dir, "cat <<'EOF' > multi.txt\nalpha\nbeta\nEOF\ncat multi.txt", 5*time.Second)
	if res.Err != nil {
		t.Fatalf("%v (%q)", res.Err, res.Output)
	}
	if res.Output != "alpha\nbeta\n" {
		t.Fatalf("output=%q", res.Output)
	}
}

// Scanning cost must be linear in output size. The rescan-per-read shape this
// replaced needed ~88s for 7 MiB, which a 120s foreground deadline cannot absorb.
func TestPersistentShellLargeOutputStaysLinear(t *testing.T) {
	skipNonPOSIX(t)
	if testing.Short() {
		t.Skip("large-output timing")
	}
	sh := posixShell(t)
	dir := t.TempDir()
	m := testManager(t)
	start := time.Now()
	res := runPersistent(t, m, sh, dir, "seq 1 1000000", 120*time.Second)
	elapsed := time.Since(start)
	if res.Err != nil {
		t.Fatalf("%v", res.Err)
	}
	if len(res.Output) < 6_800_000 {
		t.Fatalf("output truncated at %d bytes", len(res.Output))
	}
	// Generous bound: the defect this pins was two orders of magnitude over it.
	if elapsed > 20*time.Second {
		t.Fatalf("6.9 MiB of output took %s", elapsed)
	}
	t.Logf("6.9 MiB in %s", elapsed.Round(time.Millisecond))
}

// Output beyond the shared cap is truncated, not turned into a dead shell.
func TestPersistentShellBoundsHugeOutput(t *testing.T) {
	skipNonPOSIX(t)
	if testing.Short() {
		t.Skip("large-output bound")
	}
	sh := posixShell(t)
	m := testManager(t)
	dir := t.TempDir()
	res := runPersistent(t, m, sh, dir, "head -c 12000000 /dev/zero | tr '\\0' 'x'; printf '\\n'", 120*time.Second)
	if res.Err != nil {
		t.Fatalf("%v", res.Err)
	}
	if res.ShellDied {
		t.Fatal("crossing the output cap must not kill the session shell")
	}
	if !strings.Contains(res.Output, "truncated at 10 MiB") {
		t.Fatalf("expected the shared truncation notice, got %d bytes", len(res.Output))
	}
	after := runPersistent(t, m, sh, dir, "printf 'alive\\n'", 5*time.Second)
	if after.Output != "alive\n" {
		t.Fatalf("shell unusable after truncation: %q", after.Output)
	}
}

func TestPersistentShellSurvivesRepeatedCommands(t *testing.T) {
	skipNonPOSIX(t)
	sh := posixShell(t)
	dir := t.TempDir()
	m := testManager(t)
	for i := range 20 {
		res := runPersistent(t, m, sh, dir, fmt.Sprintf("printf '%%s\\n' %d", i), 5*time.Second)
		if res.Err != nil || res.Output != fmt.Sprintf("%d\n", i) {
			t.Fatalf("iteration %d: output=%q err=%v", i, res.Output, res.Err)
		}
	}
}

func TestInteractiveArgvKinds(t *testing.T) {
	if got := InteractiveArgv(sandbox.Shell{Kind: sandbox.ShellPowerShell, Path: "pwsh"}); got[0] != "pwsh" {
		t.Fatalf("argv=%v", got)
	}
}
