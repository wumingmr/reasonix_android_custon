package gitcmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func hasConfig(args []string, want string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c" && args[i+1] == want {
			return true
		}
	}
	return false
}

func TestArgsCarryBaselineConfig(t *testing.T) {
	args := argsFor("linux", "/repo", nil, "rev-parse", "--show-toplevel")
	for _, want := range []string{"core.fsmonitor=", "maintenance.auto=false", "gc.auto=0", "core.hooksPath=" + os.DevNull, "log.showSignature=false", "merge.verifySignatures=false", "diff.submodule=short", "safe.bareRepository=explicit"} {
		if !hasConfig(args, want) {
			t.Fatalf("args = %v, want -c %s", args, want)
		}
	}
	if i := slices.Index(args, "-C"); i < 0 || args[i+1] != "/repo" {
		t.Fatalf("args = %v, want -C /repo", args)
	}
	// Caller arguments stay last and in order.
	if got := args[len(args)-2:]; got[0] != "rev-parse" || got[1] != "--show-toplevel" {
		t.Fatalf("trailing args = %v, want the caller's arguments last", got)
	}
}

// A user's own submodule.recurse=true never carries a host checkout, merge or
// reset into a submodule; other subcommands leave that choice alone.
func TestArgsPinSubmoduleRecurseForTreeUpdates(t *testing.T) {
	for _, sub := range []string{"checkout", "switch", "restore", "reset", "merge", "read-tree"} {
		if args := argsFor("linux", "/repo", nil, sub); !hasConfig(args, "submodule.recurse=false") {
			t.Fatalf("%s args = %v, want submodule.recurse=false", sub, args)
		}
	}
	if args := argsFor("linux", "/repo", nil, "status"); hasConfig(args, "submodule.recurse=false") {
		t.Fatalf("status args = %v, want the user's submodule.recurse left alone", args)
	}
}

// Extra config may add to the baseline but must never replace it: a call site
// that wants its own preference still gets the hardening.
func TestArgsExtraConfigCannotDropBaseline(t *testing.T) {
	args := argsFor("linux", "/repo", []string{"core.quotepath=false", ""}, "status")
	if !hasConfig(args, "core.fsmonitor=") {
		t.Fatalf("args = %v, want the baseline retained alongside extra config", args)
	}
	if !hasConfig(args, "core.quotepath=false") {
		t.Fatalf("args = %v, want the extra config applied", args)
	}
	base := slices.Index(args, "core.fsmonitor=")
	extra := slices.Index(args, "core.quotepath=false")
	if base > extra {
		t.Fatalf("args = %v, want baseline before extra config so the caller's value wins ties", args)
	}
	if slices.Contains(args, "") {
		t.Fatalf("args = %v, want empty config entries dropped", args)
	}
}

func TestArgsEnableLongPathsOnlyOnWindows(t *testing.T) {
	if args := argsFor("windows", `C:\Users\test\repo`, nil, "status"); !hasConfig(args, "core.longpaths=true") {
		t.Fatalf("windows args = %v, want core.longpaths=true", args)
	}
	if args := argsFor("linux", "/tmp/repo", nil, "status"); hasConfig(args, "core.longpaths=true") {
		t.Fatalf("non-windows args = %v, must not override core.longpaths", args)
	}
}

// The disabling flags go right after the subcommand — found past any global
// options the caller put in args — and are never duplicated.
func TestSubcommandFlagsDisableRepositoryConfiguredPrograms(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want []string
		not  []string
	}{
		{[]string{"diff", "--numstat", "HEAD", "--"}, []string{"diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty", "--numstat", "HEAD", "--"}, nil},
		{[]string{"-C", "/r", "diff", "HEAD"}, []string{"-C", "/r", "diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty", "HEAD"}, nil},
		{[]string{"-c", "user.name=x", "log", "-p"}, []string{"-c", "user.name=x", "log", "--no-ext-diff", "--no-textconv", "-p"}, []string{"--ignore-submodules=dirty"}},
		{[]string{"--git-dir=/g", "show", "HEAD"}, []string{"--git-dir=/g", "show", "--no-ext-diff", "--no-textconv", "HEAD"}, nil},
		{[]string{"-C", "/r", "status", "--porcelain=v1"}, []string{"-C", "/r", "status", "--ignore-submodules=dirty", "--porcelain=v1"}, []string{"--no-ext-diff"}},
		{[]string{"diff", "--no-ext-diff", "--ignore-submodules=all"}, []string{"diff", "--no-textconv", "--no-ext-diff", "--ignore-submodules=all"}, nil},
		{[]string{"rev-parse", "--show-toplevel"}, []string{"rev-parse", "--show-toplevel"}, nil},
		{[]string{"--version"}, []string{"--version"}, nil},
	} {
		got := hardenSubcommand(tt.args)
		if !slices.Equal(got, tt.want) {
			t.Fatalf("hardenSubcommand(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

func TestDetachedRunsOutsideAnyRepository(t *testing.T) {
	requirePOSIXGit(t)
	f := newRepoFixture(t, "", map[string]string{"f.txt": "x\n"})
	t.Chdir(f.dir)
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Env = append(os.Environ(), "GIT_DIR="+filepath.Join(f.dir, ".git"))
	cleanup, err := Detached(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("rev-parse in a detached command found a repository: %s", out)
	}
	if slices.Contains(cmd.Env, "GIT_DIR="+filepath.Join(f.dir, ".git")) {
		t.Fatalf("env = %v, want GIT_DIR dropped", cmd.Env)
	}
}

func TestEnvDisablesPromptsAndKeepsSSHUsable(t *testing.T) {
	env := Env()
	if !slices.Contains(env, "GIT_OPTIONAL_LOCKS=0") || !slices.Contains(env, "GIT_TERMINAL_PROMPT=0") || !slices.Contains(env, "GIT_NO_LAZY_FETCH=1") {
		t.Fatalf("env = %v, want optional locks and terminal prompts disabled", env)
	}
	// An empty value is a *present* value to git: clearing these would break
	// legitimate ssh remotes and external diff tooling rather than harden.
	for _, banned := range []string{"GIT_SSH_COMMAND=", "GIT_EXTERNAL_DIFF="} {
		if slices.Contains(env, banned) {
			t.Fatalf("env = %v, must not set %q", env, banned)
		}
	}
}

// The invariant this package exists for: a repository's own config names a
// command in core.fsmonitor, and inspecting that repository must not run it.
// git executes fsmonitor during an index refresh, which a plain status does.
func TestRepositoryConfigCannotRunCommandsDuringInspection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("payload script is POSIX shell")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	repo := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	run := func(args ...string) {
		t.Helper()
		if out, err := Command(ctx, repo, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "--quiet")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "file.txt")
	run("commit", "--quiet", "-m", "initial")

	// A repository whose config points fsmonitor at a command. Writing the
	// marker is what an attacker's payload would do first.
	marker := filepath.Join(t.TempDir(), "executed")
	payload := filepath.Join(t.TempDir(), "payload.sh")
	script := "#!/bin/sh\ntouch " + marker + "\nexit 1\n"
	if err := os.WriteFile(payload, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	run("config", "core.fsmonitor", payload)

	// Dirty the tree so an index refresh has work to do, then inspect it the
	// way the status readout does.
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = Command(ctx, repo, "status", "--porcelain=v1").CombinedOutput()
	_, _ = Command(ctx, repo, "diff", "--numstat", "HEAD", "--").CombinedOutput()
	_, _ = Command(ctx, repo, "rev-parse", "--show-toplevel").CombinedOutput()

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("repository config ran a command during inspection")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat marker: %v", err)
	}
}

// The clean-filter residual from the advisory: a .gitattributes entry plus a
// filter.<driver>.clean command in the repository's local config makes
// `git diff` run that command to produce the "clean" working-tree side, and
// neither --no-ext-diff nor --no-textconv covers it. Diff invocations must
// neutralize every locally-defined driver while still rendering a correct
// diff (the emptied filter is an identity pass-through, not a content wipe).
func TestDiffDoesNotRunRepositoryCleanFilters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("payload script is POSIX shell")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	repo := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	run := func(args ...string) {
		t.Helper()
		if out, err := Command(ctx, repo, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "--quiet")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")

	marker := filepath.Join(t.TempDir(), "executed")
	payload := filepath.Join(t.TempDir(), "clean.sh")
	script := "#!/bin/sh\ntouch " + marker + "\ncat\n"
	if err := os.WriteFile(payload, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("secret.bin filter=pwn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "secret.bin"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", ".gitattributes", "secret.bin")
	run("commit", "--quiet", "-m", "initial")
	run("config", "filter.pwn.clean", payload)
	run("config", "filter.pwn.process", payload)
	run("config", "filter.pwn.required", "true")
	if err := os.WriteFile(filepath.Join(repo, "secret.bin"), []byte("secret\nchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The exact shape desktop/workspace_changes.go builds: -C inside args.
	out, err := Command(ctx, "", "-C", repo, "diff", "--no-ext-diff", "--no-textconv", "--relative", "HEAD", "--", filepath.FromSlash("secret.bin")).CombinedOutput()
	if err != nil {
		t.Fatalf("diff failed: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "changed") {
		t.Fatalf("diff output lost the working-tree change (filter neutralization must pass content through):\n%s", out)
	}
	// And the shape internal/cli/gitstatus.go builds: dir parameter + diff.
	if out, err = Command(ctx, repo, "diff", "--numstat", "HEAD", "--").CombinedOutput(); err != nil {
		t.Fatalf("numstat diff failed: %v: %s", err, out)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("repository clean filter ran during a diff")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat marker: %v", err)
	}
}
