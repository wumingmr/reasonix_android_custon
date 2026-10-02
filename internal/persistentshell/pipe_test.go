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

// Run the Windows byte-stream transport on every host with Bash installed.
// On Windows use the production dispatcher, so accidentally restoring ConPTY
// cannot pass this regression merely because startPipe still works in isolation.
func newPipeTestManager(t *testing.T) (*Manager, Request) {
	t.Helper()
	sh, ok := sandbox.ResolveExplicitBash("")
	if !ok {
		t.Skip("native Bash is required for the pipe transport regression")
	}
	dir := t.TempDir()
	env := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !strings.EqualFold(key, "LC_ALL") && !strings.EqualFold(key, "LANG") && !strings.EqualFold(key, "BASH_ENV") {
			env = append(env, item)
		}
	}
	env = append(env, "LC_ALL=C", "TERM=dumb")
	req := Request{Shell: sh, Argv: interactiveArgvForOS(sh, "windows"), Dir: dir, Env: env, Timeout: 5 * time.Second}
	var s *session
	var err error
	if runtime.GOOS == "windows" {
		req.Argv = InteractiveArgv(sh)
		s, err = startSession(req, fingerprint(req))
	} else {
		s, err = startPOSIXSession(req, fingerprint(req), startPipe)
	}
	if err != nil {
		t.Fatal(err)
	}
	m := testManager(t)
	m.live = s
	if _, ok := s.conn.(*pipeProcess); !ok {
		t.Fatalf("Windows Bash must use pipes, got %T", s.conn)
	}
	return m, req
}

func TestBashPipeUnicodeCompletionAndState(t *testing.T) {
	m, req := newPipeTestManager(t)
	filename := "中文😀 '文件.txt"
	if err := os.WriteFile(filepath.Join(req.Dir, filename), []byte("内容😀"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(req.Dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(command, want string) {
		t.Helper()
		req.Command = command
		res := m.Run(t.Context(), req)
		if res.Err != nil || !res.ExitCodeKnown || res.ExitCode != 0 || res.Reset || res.Output != want {
			t.Fatalf("command=%q result=%+v, want output=%q", command, res, want)
		}
	}
	// Both the interpreter and native utilities must see pipes, never a console.
	run("test ! -t 0 && test ! -t 1 && test ! -t 2", "")
	run("ls -a", ".\n..\nsub\n"+filename+"\n")
	run("cat "+posixQuote(filename), "内容😀")
	// Exercise output well beyond the former 80-column screen and read boundary.
	long := strings.Repeat("中文😀", 2000)
	run("printf '%s' "+posixQuote(long), long)
	run("printf '前'; printf '错误' >&2; printf '后'", "前错误后")
	run("cd sub; export REASONIX_PIPE_STATE='变量😀'; rx_pipe_fn() { printf '%s' \"$REASONIX_PIPE_STATE\"; }", "")
	run("printf '%s:' \"${PWD##*/}\"; rx_pipe_fn", "sub:变量😀")
	req.Command = "printf '失败中文' >&2; false"
	failed := m.Run(t.Context(), req)
	if failed.Err == nil || !failed.ExitCodeKnown || failed.ExitCode != 1 || failed.Reset || failed.Output != "失败中文" {
		t.Fatalf("non-zero completion was lost: %+v", failed)
	}
	run("rx_pipe_fn", "变量😀")
	// Commands sharing this manager are serialized; output must stay with its call.
	results := make(chan Result, 4)
	for i := range 4 {
		go func() {
			r := req
			r.Command = fmt.Sprintf("printf '调用%d中文'", i)
			results <- m.Run(context.Background(), r)
		}()
	}
	seen := map[string]bool{}
	for range 4 {
		res := <-results
		if res.Err != nil || !res.ExitCodeKnown || res.ExitCode != 0 || seen[res.Output] {
			t.Fatalf("mixed concurrent output: %+v", res)
		}
		seen[res.Output] = true
	}
	for i := range 4 {
		if !seen[fmt.Sprintf("调用%d中文", i)] {
			t.Fatalf("missing call %d: %v", i, seen)
		}
	}
}

func TestBashPipeCancellationReapsShell(t *testing.T) {
	m, req := newPipeTestManager(t)
	p := m.live.conn.(*pipeProcess)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req.Command = "printf 'started\\n%0128d\\n' 0; sleep 30"
	req.Progress = &cancelOnStarted{cancel: cancel}
	res := m.Run(ctx, req)
	if !res.Canceled || !res.Reset || m.live != nil {
		t.Fatalf("cancel did not reset pipe session: %+v", res)
	}
	if p.cmd.ProcessState == nil || p.cmd.ProcessState.Success() {
		t.Fatal("cancel did not kill and reap shell")
	}
	if _, err := p.Write([]byte("echo stale\n")); err == nil {
		t.Fatal("retired pipe still accepts commands")
	}
}

func TestBashPipeTimeoutResetsSession(t *testing.T) {
	m, req := newPipeTestManager(t)
	req.Command = "sleep 30"
	req.Timeout = 100 * time.Millisecond
	res := m.Run(t.Context(), req)
	if !res.TimedOut || !res.Reset || res.ExitCodeKnown || m.live != nil {
		t.Fatalf("timeout must retire the session without claiming completion: %+v", res)
	}
}
