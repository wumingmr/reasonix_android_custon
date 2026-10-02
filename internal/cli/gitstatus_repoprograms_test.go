package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The status readout inspects a repository an agent can write into, so none
// of the programs that repository's config names may run while it is read —
// and the readout must still be right. Startup runs it from a subdirectory.
func TestLoadGitStatusDoesNotRunRepositoryConfiguredPrograms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("payload script is POSIX shell")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")
	if err := os.Mkdir(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(".gitattributes", "f.txt filter=pwn diff=pwn\n")
	write("f.txt", "hello\n")
	write("sub/keep.txt", "keep\n")
	git("add", ".")
	git("commit", "-qm", "base")

	marker := filepath.Join(t.TempDir(), "executed")
	payload := filepath.Join(t.TempDir(), "payload.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ran >> '"+marker+"'\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.WriteString("[filter.pwn]\n\tclean = " + payload + "\n[diff \"pwn\"]\n\ttextconv = " + payload + "\n")
	_ = cfg.Close()
	if err != nil {
		t.Fatal(err)
	}
	write("f.txt", "hellx\n")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(repo, "f.txt"), old, old); err != nil {
		t.Fatal(err)
	}

	status, err := loadGitStatus(context.Background(), openedRepo(t, filepath.Join(repo, "sub")))
	if err != nil {
		t.Fatal(err)
	}
	if status.Added != 1 || status.Removed != 1 {
		t.Fatalf("status = %+v, want +1 -1 for the same-size edit", status)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the status readout ran a repository-configured program")
	}
}

// The status line reads the repository the session resolved when it opened,
// even after the workspace — a subdirectory of it — gains its own .git whose
// local config names a filter.
func TestLoadGitStatusKeepsRepositoryResolvedAtOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("payload script is POSIX shell")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
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
	session := openedRepo(t, pkg)

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

	status, err := loadGitStatus(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if status.Repo != filepath.Base(mono) || status.Added != 1 || status.Removed != 1 {
		t.Fatalf("status = %+v, want the enclosing repository's +1 -1", status)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the status readout ran the nested repository's filter")
	}
}
