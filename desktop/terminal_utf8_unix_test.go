//go:build !windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTerminalBashUnicodeInput(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"", "C", "en_US.UTF-8"} {
		t.Run("locale="+locale, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "中文 custom inputrc")
			if err := os.WriteFile(source, []byte("set editing-mode vi\nset convert-meta on\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "INPUTRC=" + source}
			if locale != "" {
				env = append(env, "LC_ALL="+locale)
			}
			p, err := startTerminalProcess(terminalStartSpec{command: commandForShellPath(bash, "bash"), dir: dir, env: terminalEnvironment(env), cols: 80, rows: 24})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close(); _, _ = p.Wait() })
			chunks := make(chan []byte, 128)
			go func() {
				defer close(chunks)
				buf := make([]byte, 4096)
				for {
					n, err := p.Read(buf)
					if n > 0 {
						chunks <- bytes.Clone(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}()
			readUntil := func(marker string) string {
				t.Helper()
				var output strings.Builder
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				for {
					select {
					case data, ok := <-chunks:
						if !ok {
							t.Fatalf("terminal closed: %q", output.String())
						}
						output.Write(data)
						if strings.Contains(output.String(), marker) {
							return output.String()
						}
					case <-timer.C:
						t.Fatalf("terminal timeout: %q", output.String())
					}
				}
			}
			write := func(command string) {
				t.Helper()
				if _, err := p.Write([]byte(command + "\n")); err != nil {
					t.Fatal(err)
				}
			}
			write("stty -echo; PS1=; PS2=; printf '\\nREADY\\n'")
			readUntil("\r\nREADY\r\n")
			write("printf '%s\\n' '中文😀'; bind -v | grep 'editing-mode'; printf 'LOCALE=%s\\nDONE\\n' \"$LC_ALL\"")
			out := readUntil("\r\nDONE\r\n")
			if !strings.Contains(out, "中文😀\r\n") || !strings.Contains(out, "editing-mode vi") || !strings.Contains(out, "LOCALE="+locale+"\r\n") {
				t.Fatalf("input/configuration changed: %q", out)
			}
		})
	}
}

func TestTerminalInputrcLifecycle(t *testing.T) {
	spec := terminalStartSpec{command: terminalCommand{path: "/bin/bash"}, dir: t.TempDir(), env: []string{"HOME=" + t.TempDir()}}
	env, cleanup, err := terminalInputEnvironment(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	var path string
	for _, item := range env {
		if value, ok := strings.CutPrefix(item, "INPUTRC="); ok {
			path = value
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("unsafe mode %v", info.Mode())
	}
	cleanup()
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("inputrc not removed: %v", err)
	}
}
