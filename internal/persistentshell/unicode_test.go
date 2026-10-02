package persistentshell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Desktop launches can omit all locale variables. The command must reach
// interactive Bash unchanged even when Readline treats high bytes as meta keys.
func TestPersistentShellUnicodeTransport(t *testing.T) {
	skipNonPOSIX(t)
	sh := posixShell(t)
	for _, locale := range []string{"unset", "C", "en_US.UTF-8"} {
		t.Run(locale, func(t *testing.T) {
			dir := t.TempDir()
			m := testManager(t)
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TERM=dumb", "INPUTRC=/dev/null"}
			if locale != "unset" {
				env = append(env, "LC_ALL="+locale)
			}
			req := Request{Argv: InteractiveArgv(sh), Dir: dir, Env: env, Shell: sh, Timeout: 10 * time.Second}
			run := func(command, want string) {
				t.Helper()
				req.Command = command
				res := m.Run(context.Background(), req)
				if res.Err != nil || res.ExitCode != 0 || res.Output != want {
					t.Fatalf("command (%d bytes): err=%v exit=%d output=%q, want %q", len(command), res.Err, res.ExitCode, res.Output, want)
				}
			}
			run("printf '%s' '起点是空白格😀7'", "起点是空白格😀7")
			// Exercise the incident's grep shape using a disposable Unicode path.
			if err := os.WriteFile(filepath.Join(dir, "中文 '文件.txt"), []byte("起点是空白格\nother\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			run("grep -n '起点是空白格' "+posixQuote("中文 '文件.txt"), "1:起点是空白格\n")
			text := "中文 'quote' \\ $(false) 😀"
			run("value="+posixQuote(text)+"; printf '%s' \"$value\"", text)
			run("printf '%s' \"$value\"", text)
			run("cat <<'EOF'\n中文😀\nEOF", "中文😀\n")
			// The ASCII encoding expands non-ASCII input fourfold. Exercise a
			// wrapper well beyond a canonical terminal line buffer and read chunk.
			long := strings.Repeat("中文😀", 2000)
			run("printf '%s' "+posixQuote(long), long)
			// Locale is user command state, not something transport may replace.
			run("export LC_ALL=C", "")
			run("printf '%s' \"$LC_ALL\"; printf '%s' '中文'", "C中文")

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Exceed the capture's marker overlap so progress is delivered
			// while the command is running, not only when partial output flushes.
			req.Command = "printf 'started\\n%0128d\\n' 0; sleep 30"
			req.Progress = &cancelOnStarted{cancel: cancel}
			res := m.Run(ctx, req)
			if !res.Canceled || !res.Reset {
				t.Fatalf("cancel must retire the running shell: %+v", res)
			}
			req.Progress = nil
			run("printf '%s' '恢复😀'", "恢复😀")
		})
	}
}

type cancelOnStarted struct {
	output strings.Builder
	cancel context.CancelFunc
}

// Between prompts the PTY can be in canonical mode. Disabling line editing
// keeps that state deterministic and must not truncate an encoded command.
func TestPersistentShellLongCommandWithoutLineEditing(t *testing.T) {
	skipNonPOSIX(t)
	sh := posixShell(t)
	dir := t.TempDir()
	m := testManager(t)
	argv := append([]string{sh.Path, "--noediting"}, InteractiveArgv(sh)[1:]...)
	req := Request{Argv: argv, Dir: dir, Shell: sh, Timeout: 10 * time.Second,
		Env:     []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TERM=dumb", "INPUTRC=/dev/null", "LC_ALL=C"},
		Command: "PS2=CONTINUATION_PROMPT"}
	if res := m.Run(t.Context(), req); res.Err != nil {
		t.Fatal(res.Err)
	}
	text := strings.Repeat("中文😀", 2000)
	req.Command = "value=" + posixQuote(text) + "; printf '%s' \"$value\""
	res := m.Run(t.Context(), req)
	if res.Err != nil || res.Output != text {
		t.Fatalf("long command: err=%v output bytes=%d want=%d", res.Err, len(res.Output), len(text))
	}
	req.Command = "printf '%s' \"$value\""
	if res := m.Run(t.Context(), req); res.Err != nil || res.Output != text {
		t.Fatalf("retained value: err=%v output bytes=%d want=%d", res.Err, len(res.Output), len(text))
	}
}

func (w *cancelOnStarted) Write(p []byte) (int, error) {
	w.output.Write(p)
	if strings.Contains(w.output.String(), "started\n") {
		w.cancel()
	}
	return len(p), nil
}
