package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"reasonix/internal/gitcmd"
)

// partialClone returns a blob-filtered clone of a three-commit repository, so
// the blobs of every commit but the checked-out one are absent locally.
func partialClone(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	src := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(src, "init", "-q")
	for _, body := range []string{"1\n", "2\n", "3\n"} {
		if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		run(src, "add", "a.txt")
		run(src, "commit", "-q", "-m", body)
	}
	run(src, "config", "uploadpack.allowFilter", "true")
	clone := filepath.Join(t.TempDir(), "partial")
	run(src, "clone", "-q", "--filter=blob:none", "file://"+src, clone)
	return clone
}

// A historical patch in a partial clone fails with an error naming the
// unfetched objects, which the commit-detail view can tell from other failures.
func TestCommitDetailInPartialCloneReportsObjectNotLocal(t *testing.T) {
	clone := partialClone(t)
	out, err := workspaceGit(openWorkspaceRepo(clone), "-C", clone, "show", "--pretty=format:", "--patch", "HEAD~1", "--", "a.txt").Output()
	if err == nil {
		t.Fatalf("show of an unfetched commit succeeded: %s", out)
	}
	if got := notLocalOr(openWorkspaceRepo(clone), "HEAD~1", err); !errors.Is(got, gitcmd.ErrObjectNotLocal) {
		t.Fatalf("notLocalOr = %v, want ErrObjectNotLocal", got)
	}
	other := errors.New("other")
	if got := notLocalOr(openWorkspaceRepo(clone), "no-such-rev", other); !errors.Is(got, other) {
		t.Fatalf("notLocalOr(no-such-rev) = %v, want the original error passed through", got)
	}
}
