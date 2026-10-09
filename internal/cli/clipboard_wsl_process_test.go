package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWSLClipboardPreservesLinuxBackends(t *testing.T) {
	for _, tc := range []struct {
		name     string
		wayland  string
		programs []string
		want     string
		args     string
	}{
		{"wayland", "wayland-0", []string{"wl-copy", "xclip", "xsel"}, "wl-copy", ""},
		{"xclip", "", []string{"xclip", "xsel"}, "xclip", "-in -selection clipboard"},
		{"prefer Linux to Windows", "", []string{"xclip", "powershell.exe"}, "xclip", "-in -selection clipboard"},
		{"xsel", "", []string{"xsel"}, "xsel", "--input --clipboard"},
		{"missing wayland utility", "wayland-0", []string{"xclip"}, "xclip", "-in -selection clipboard"},
		{"no wayland session", "", []string{"wl-copy", "xclip"}, "xclip", "-in -selection clipboard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupWSLClipboardProcessTest(t)
			t.Setenv("WAYLAND_DISPLAY", tc.wayland)
			for _, name := range tc.programs {
				installWSLClipboardFixture(t, dir, name, "printf '%s' '"+name+"' > \"$CLIPBOARD_TEST_BACKEND\"\nprintf '%s' \"$*\" > \"$CLIPBOARD_TEST_ARGS\"\n/bin/cat > \"$CLIPBOARD_TEST_TEXT\"\n")
			}
			text := "中文\n你好 🚀\r\n'\"; $(not-a-command)\n"
			if err := writeWSLClipboardText(text); err != nil {
				t.Fatalf("copy with a working Linux backend but no PowerShell: %v", err)
			}
			assertWSLClipboardFile(t, "CLIPBOARD_TEST_TEXT", text)
			assertWSLClipboardFile(t, "CLIPBOARD_TEST_BACKEND", tc.want)
			assertWSLClipboardFile(t, "CLIPBOARD_TEST_ARGS", tc.args)
		})
	}
}

func TestWSLClipboardBridgeProcess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			dir := setupWSLClipboardProcessTest(t)
			script := "/bin/cat > \"$CLIPBOARD_TEST_TEXT\"\n"
			installWSLClipboardFixture(t, dir, "clip.exe", "exit 99\n")
			if fail {
				script += "echo clipboard-unavailable >&2\nexit 7\n"
			}
			installWSLClipboardFixture(t, dir, "powershell.exe", script)
			text := "中文\n你好 🚀\r\n'\"; $(not-a-command)\n"
			err := writeWSLClipboardText(text)
			if fail {
				if err == nil || !strings.Contains(err.Error(), "clipboard-unavailable") {
					t.Fatalf("expected command failure with stderr, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			assertWSLClipboardFile(t, "CLIPBOARD_TEST_TEXT", text)
		})
	}
}

func TestWSLClipboardCommandCancellation(t *testing.T) {
	dir := setupWSLClipboardProcessTest(t)
	installWSLClipboardFixture(t, dir, "powershell.exe", "echo ready >&2\nexec /bin/sleep 60\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := newWSLClipboardCommand(ctx, "中文")
	// Cancel only after the child is running, without relying on startup timing.
	cmd.Stderr = clipboardCancelWriter{cancel: cancel}
	if err := cmd.Run(); err == nil {
		t.Fatal("a canceled clipboard command succeeded")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("command did not respond to cancellation: %v", ctx.Err())
	}
	if cmd.ProcessState == nil {
		t.Fatal("canceled clipboard child was not reaped")
	}
}

type clipboardCancelWriter struct{ cancel context.CancelFunc }

func (w clipboardCancelWriter) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

func setupWSLClipboardProcessTest(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("WSL command execution uses POSIX executable fixtures")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "")
	for _, key := range []string{"CLIPBOARD_TEST_TEXT", "CLIPBOARD_TEST_BACKEND", "CLIPBOARD_TEST_ARGS"} {
		t.Setenv(key, filepath.Join(dir, key))
	}
	return dir
}

// wslClipboardFixtureShell returns an interpreter able to run the clipboard
// fixtures. It deliberately does not consult PATH: these tests set PATH to an
// empty directory so that only the fixtures are found, so a PATH lookup here
// would fail and silently skip every case — a skip the parent test still reports
// as PASS.
//
// /bin/sh is not portable for this. On Android it is toybox, whose printf is not
// a builtin, so every `printf` in the fixtures fails with "printf: inaccessible
// or not found" and each backend assertion then reads an empty file. Termux's
// own shell — and any ordinary POSIX host's — runs them correctly.
func wslClipboardFixtureShell(t *testing.T) string {
	t.Helper()
	var candidates []string
	if prefix := os.Getenv("PREFIX"); prefix != "" {
		candidates = append(candidates, filepath.Join(prefix, "bin", "sh"))
	}
	candidates = append(candidates, "/usr/bin/sh", "/bin/dash")
	for _, sh := range candidates {
		if info, err := os.Stat(sh); err == nil && !info.IsDir() {
			return sh
		}
	}
	if sh, err := exec.LookPath("bash"); err == nil {
		return sh
	}
	// Fail loudly: silently skipping would turn a real regression into a green
	// test run.
	t.Fatal("no POSIX shell available to run the clipboard fixtures")
	return ""
}

func installWSLClipboardFixture(t *testing.T, dir, name, script string) {
	t.Helper()
	shell := wslClipboardFixtureShell(t)
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!"+shell+"\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
}

func assertWSLClipboardFile(t *testing.T, key, want string) {
	t.Helper()
	got, err := os.ReadFile(os.Getenv(key))
	if err != nil || string(got) != want {
		t.Fatalf("%s = %q, %v; want %q", key, got, err, want)
	}
}
