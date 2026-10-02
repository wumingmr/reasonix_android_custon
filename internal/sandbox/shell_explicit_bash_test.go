package sandbox

import (
	"os/exec"
	"testing"
)

func TestExplicitBashPreservesHookDialectWithoutPowerShellFallback(t *testing.T) {
	const bashPath = `C:\Git\bin\bash.exe`
	snap := &shellSnapshot{
		goos:       "windows",
		lookPath:   func(string) (string, error) { return "", exec.ErrNotFound },
		exists:     func(path string) bool { return path == bashPath },
		isWSL:      func(string) bool { return false },
		bashCands:  []string{bashPath},
		probeFunc:  func(path string) bool { return path == bashPath },
		probeCache: make(map[string]bool),
	}
	got, ok := resolveExplicitBash(snap, "")
	if !ok || got.Kind != ShellBash || got.Path != bashPath {
		t.Fatalf("explicit hook shell = %+v, found=%v", got, ok)
	}
	snap.bashCands = nil
	if _, ok := resolveExplicitBash(snap, ""); ok {
		t.Fatal("missing explicit Bash must not fall back to PowerShell")
	}
}
