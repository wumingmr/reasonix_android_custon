package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/gitcmd"
)

// The changes panel reads a tab through the identity its session resolved when
// it opened, even after the workspace — a subdirectory of that repository —
// gains its own .git whose local config names a filter.
func TestWorkspaceChangesReadTheSessionRepository(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("payload script is POSIX shell")
	}
	mono := t.TempDir()
	pkg := filepath.Join(mono, "pkg")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(mono, "init", "-q")
	git(mono, "add", ".")
	git(mono, "commit", "-qm", "base")
	repo, err := gitcmd.Open(context.Background(), pkg)
	if err != nil {
		t.Fatal(err)
	}
	ctrl := control.New(control.Options{Sink: event.Discard, WorkspaceRoot: pkg, WorkspaceRepo: repo})
	a := &App{tabs: map[string]*WorkspaceTab{"a": {WorkspaceRoot: pkg, Ctrl: ctrl}}}

	marker := filepath.Join(t.TempDir(), "executed")
	payload := filepath.Join(t.TempDir(), "payload.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ran >> '"+marker+"'\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(pkg, "init", "-q")
	git(pkg, "config", "filter.pwn.clean", payload)
	if err := os.WriteFile(filepath.Join(pkg, ".gitattributes"), []byte("f.txt filter=pwn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "f.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	view := a.WorkspaceChanges("a")
	var modified bool
	for _, f := range view.Files {
		modified = modified || (f.Path == "f.txt" && f.GitStatus == "M")
	}
	if !view.GitAvailable || !modified || view.Removed != 1 {
		t.Fatalf("changes = %+v, want f.txt modified against the enclosing repository", view)
	}
	if _, err := a.WorkspaceChangeDetail("a", "f.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the changes panel ran the nested repository's filter")
	}
}
