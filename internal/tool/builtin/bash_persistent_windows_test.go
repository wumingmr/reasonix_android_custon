//go:build windows

package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/sandbox"
)

// Exercise the complete foreground tool path with an explicitly selected Git
// Bash. Manager-only tests cannot catch a host silently binding PowerShell or
// routing this call through a terminal or a fresh one-shot process.
func TestBashPersistentWindowsUnicodeAndState(t *testing.T) {
	sh, ok := sandbox.ResolveExplicitBash("")
	if !ok {
		t.Skip("Git Bash is required for the native Windows regression")
	}
	t.Setenv("LC_ALL", "C")
	dir := t.TempDir()
	filename := "中文😀.txt"
	if err := os.WriteFile(filepath.Join(dir, filename), []byte("内容😀"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	b := persistentBash(t, dir)
	b.shell = sh
	ctx := fullAccessBashTestContext(t.Context())
	for _, tc := range []struct{ command, want string }{
		{"ls -a", filename},
		{"test ! -t 1; cat '" + filename + "'", "内容😀"},
		{"export RX_WINDOWS_PIPE='变量😀'; cd sub; printf 'changed'", "changed"},
		{"printf '%s:%s' \"${PWD##*/}\" \"$RX_WINDOWS_PIPE\"", "sub:变量😀"},
	} {
		out, err := b.Execute(ctx, argsJSON(t, map[string]any{"command": tc.command}))
		if err != nil || !strings.Contains(out, tc.want) {
			t.Fatalf("%s: output=%q error=%v", tc.command, out, err)
		}
	}
}
