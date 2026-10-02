package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Create checks the new worktree out. That checkout runs in a child whose
// gitdir is the new worktree, so a conditional include keyed on that gitdir or
// on the new branch is invisible to a driver listing done in the source
// repository — the child would still run the filter. Create must populate the
// worktree without running any such driver, and the file must arrive intact.
func TestCreateWorktreeDoesNotRunConditionallyIncludedDrivers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("payload script is POSIX shell")
	}
	requireGit(t)
	for _, cond := range []string{"gitdir:**/worktrees/**", "onbranch:reasonix/**"} {
		t.Run(cond, func(t *testing.T) {
			repo := t.TempDir()
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
				cmd.Env = append(os.Environ(),
					"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			git("init")
			if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("f.txt filter=pwn\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("hello\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git("add", "-A")
			git("commit", "-m", "initial")

			marker := filepath.Join(t.TempDir(), "executed")
			payload := filepath.Join(t.TempDir(), "payload.sh")
			if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ran >> '"+marker+"'\ncat\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			evil := filepath.Join(t.TempDir(), "evil.cfg")
			if err := os.WriteFile(evil, []byte("[filter \"pwn\"]\n\tsmudge = "+payload+"\n\tclean = "+payload+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.WriteString("[includeIf \"" + cond + "\"]\n\tpath = " + evil + "\n"); err != nil {
				t.Fatal(err)
			}
			_ = cfg.Close()

			result, err := Create(context.Background(), opened(t, repo), t.TempDir())
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(result.WorktreeRoot, "f.txt"))
			if err != nil || string(data) != "hello\n" {
				t.Fatalf("worktree f.txt = %q, %v; want the committed bytes", data, err)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("creating a worktree ran a conditionally-included driver")
			} else if !os.IsNotExist(err) {
				t.Fatalf("stat marker: %v", err)
			}
			if strings.HasPrefix(cond, "onbranch") && !strings.HasPrefix(result.Branch, "reasonix/") {
				t.Fatalf("branch = %q, want the onbranch condition to have matched", result.Branch)
			}
		})
	}
}

// RollbackCreate removes an unused worktree. A bare `worktree remove` runs
// status inside the linked worktree, whose config.worktree the source-root
// listing never saw; a driver there would run. Removal must not run it.
func TestRollbackCreateDoesNotRunLinkedWorktreeDrivers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("payload script is POSIX shell")
	}
	requireGit(t)
	repo := initRepo(t)
	gitTest(t, repo, "config", "user.name", "t")
	gitTest(t, repo, "config", "user.email", "t@t")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("README.md filter=pwn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", ".gitattributes")
	gitTest(t, repo, "commit", "-m", "attrs")
	result, err := Create(context.Background(), opened(t, repo), t.TempDir())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	gitDir := strings.TrimSpace(gitTest(t, result.WorktreeRoot, "rev-parse", "--absolute-git-dir"))
	gitTest(t, result.WorktreeRoot, "config", "extensions.worktreeConfig", "true")
	marker := filepath.Join(t.TempDir(), "executed")
	payload := filepath.Join(t.TempDir(), "payload.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ran >> '"+marker+"'\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config.worktree"),
		[]byte("[filter \"pwn\"]\n\tclean = "+payload+"\n\tsmudge = "+payload+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RollbackCreate(context.Background(), result); err != nil {
		t.Fatalf("RollbackCreate: %v", err)
	}
	if _, err := os.Stat(result.WorktreeRoot); !os.IsNotExist(err) {
		t.Fatalf("worktree still present after rollback: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("removing the worktree ran a linked-worktree driver")
	}
}

// When populating the new worktree fails, Create leaves neither the worktree
// nor the branch its add created behind.
func TestCreateCleansUpBranchWhenPopulateFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX false binary")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("f.txt filter=req\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "init")
	// A user-scope required filter whose smudge fails: user scope stays live,
	// so the checkout inside the new worktree fails.
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[filter \"req\"]\n\tclean = cat\n\tsmudge = false\n\trequired = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	if _, err := Create(context.Background(), opened(t, repo), t.TempDir()); err == nil {
		t.Fatal("Create succeeded with a failing required filter, want an error")
	}
	if refs := strings.TrimSpace(git("for-each-ref", "--format=%(refname)", "refs/heads/reasonix/")); refs != "" {
		t.Fatalf("branches left behind: %s", refs)
	}
	if list := git("worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("worktrees left behind:\n%s", list)
	}
}
