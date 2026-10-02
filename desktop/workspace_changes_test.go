package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"reasonix/internal/gitcmd"
)

// The probe must not spawn a background daemon that opens a console of its own
// (#3906). Asserted as properties rather than a byte-exact argument list: the
// full invocation baseline is owned and pinned by internal/gitcmd, and pinning
// it a second time here only guaranteed this test would break whenever the
// baseline gained an entry.
func TestWorkspaceGitDisablesDaemonSpawns(t *testing.T) {
	cmd := workspaceGit(gitcmd.Repo{}, "-C", "repo", "status", "--porcelain=v1")
	for _, want := range []string{"core.fsmonitor=", "maintenance.auto=false"} {
		if !hasGitConfigArg(cmd.Args, want) {
			t.Fatalf("args = %v, want -c %s", cmd.Args, want)
		}
	}
	at := slices.Index(cmd.Args, "-C")
	if at < 0 || !slices.Equal(cmd.Args[at:at+3], []string{"-C", "repo", "status"}) || cmd.Args[len(cmd.Args)-1] != "--porcelain=v1" {
		t.Fatalf("args = %v, want the caller's arguments last and in order", cmd.Args)
	}
	if runtime.GOOS == "windows" && cmd.SysProcAttr == nil {
		t.Fatal("workspaceGit must hide the console window on Windows")
	}
}

func hasGitConfigArg(args []string, want string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c" && args[i+1] == want {
			return true
		}
	}
	return false
}

func TestWorkspaceGitBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatal(err)
		}
	}()

	repo := t.TempDir()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	runGit(t, "init")
	runGit(t, "checkout", "-b", "feature/status")

	if got := workspaceGitBranch(openWorkspaceRepo(repo)); got != "feature/status" {
		t.Fatalf("branch = %q, want feature/status", got)
	}
}

func TestWorkspaceGitBranchReflectsImmediateCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatal(err)
		}
	}()

	repo := t.TempDir()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	runGit(t, "init")
	runGit(t, "checkout", "-b", "feature/one")

	if got := workspaceGitBranch(openWorkspaceRepo(repo)); got != "feature/one" {
		t.Fatalf("branch before checkout = %q, want feature/one", got)
	}
	runGit(t, "checkout", "-b", "feature/two")
	if got := workspaceGitBranch(openWorkspaceRepo(repo)); got != "feature/two" {
		t.Fatalf("branch after checkout = %q, want feature/two", got)
	}
}

func TestWorkspaceGitBranchDetachedHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatal(err)
		}
	}()

	repo := t.TempDir()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	runGit(t, "init")
	runGit(t, "config", "user.email", "test@example.com")
	runGit(t, "config", "user.name", "Test User")
	if err := os.WriteFile("tracked.txt", []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", "tracked.txt")
	runGit(t, "commit", "-m", "init")
	short := gitOutput(t, "rev-parse", "--short", "HEAD")
	runGit(t, "checkout", "--detach", "HEAD")

	if got := workspaceGitBranch(openWorkspaceRepo(repo)); got != "@"+short {
		t.Fatalf("branch = %q, want @%s", got, short)
	}
}

func TestWorkspaceGitBranchNonGitDirectory(t *testing.T) {
	if got := workspaceGitBranch(openWorkspaceRepo(t.TempDir())); got != "" {
		t.Fatalf("branch = %q, want empty", got)
	}
}

func TestWorkspaceGitBranchForMetaDoesNotBlockOnColdProbe(t *testing.T) {
	resetWorkspaceGitBranchMetaCacheForTest(t)
	origProbe := workspaceGitBranchForMetaProbe
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseProbe := func() { releaseOnce.Do(func() { close(release) }) }
	workspaceGitBranchForMetaProbe = func(gitcmd.Repo) string {
		close(started)
		<-release
		return "feature/async"
	}
	defer func() {
		releaseProbe()
		workspaceGitBranchForMetaProbe = origProbe
	}()

	if got := workspaceGitBranchForMeta("/tmp/reasonix-cold-probe", gitcmd.Repo{}); got != "" {
		t.Fatalf("cold branch = %q, want empty while async refresh runs", got)
	}
	// The probe is still blocked here, so returning is the deterministic proof
	// that the metadata read did not wait for its refresh.
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background branch refresh did not start")
	}

	releaseProbe()
	eventuallyBranchForMeta(t, "/tmp/reasonix-cold-probe", "feature/async")
}

func TestWorkspaceGitBranchForMetaReturnsStaleDuringRefresh(t *testing.T) {
	resetWorkspaceGitBranchMetaCacheForTest(t)
	workspaceGitBranchCache.Lock()
	workspaceGitBranchCache.entries[filepath.Clean("/tmp/reasonix-stale-probe")] = workspaceGitBranchCacheEntry{
		branch:  "feature/stale",
		expires: time.Now().Add(-time.Second),
	}
	workspaceGitBranchCache.Unlock()

	origProbe := workspaceGitBranchForMetaProbe
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseProbe := func() { releaseOnce.Do(func() { close(release) }) }
	workspaceGitBranchForMetaProbe = func(gitcmd.Repo) string {
		close(started)
		<-release
		return "feature/fresh"
	}
	defer func() {
		releaseProbe()
		workspaceGitBranchForMetaProbe = origProbe
	}()

	if got := workspaceGitBranchForMeta("/tmp/reasonix-stale-probe", gitcmd.Repo{}); got != "feature/stale" {
		t.Fatalf("stale branch = %q, want feature/stale", got)
	}
	// The probe is still blocked here, so returning the stale value proves that
	// the read did not wait for its refresh.
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background branch refresh did not start")
	}

	releaseProbe()
	eventuallyBranchForMeta(t, "/tmp/reasonix-stale-probe", "feature/fresh")
}

func resetWorkspaceGitBranchMetaCacheForTest(t *testing.T) {
	t.Helper()
	workspaceGitBranchCache.Lock()
	workspaceGitBranchCache.entries = map[string]workspaceGitBranchCacheEntry{}
	workspaceGitBranchCache.Unlock()
}

func eventuallyBranchForMeta(t *testing.T, base, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := workspaceGitBranchForMeta(base, gitcmd.Repo{}); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("branch did not refresh to %q", want)
}
