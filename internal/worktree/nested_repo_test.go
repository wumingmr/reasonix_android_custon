package worktree

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// What a worktree gains after the host created it — a nested repository with
// its own local filter, a .git file naming another git dir — never runs, and
// the merge still reads the identities pinned at creation.

func markerPayload(t *testing.T) (marker, payload string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "executed")
	payload = filepath.Join(t.TempDir(), "payload.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ran >> '"+marker+"'\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return marker, payload
}

func requireMarkerAbsent(t *testing.T, marker, when string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("%s ran a repository-configured filter", when)
	}
}

func requirePOSIXPayload(t *testing.T) {
	t.Helper()
	requireGit(t)
	if runtime.GOOS == "windows" {
		t.Skip("payload script is POSIX shell")
	}
}

func TestMergeBackAutoCommitDoesNotEnterGitlinkRepository(t *testing.T) {
	requirePOSIXPayload(t)
	repo := initRepo(t)
	managed := t.TempDir()
	created, err := Create(context.Background(), opened(t, repo), managed)
	if err != nil {
		t.Fatal(err)
	}
	wt := created.WorktreeRoot
	sub := filepath.Join(wt, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	marker, payload := markerPayload(t)
	gitTest(t, sub, "init", "-q")
	gitCommitFile(t, sub, ".gitattributes", "f.txt filter=pwn\n", "attributes")
	gitCommitFile(t, sub, "f.txt", "a\n", "nested")
	gitTest(t, sub, "config", "filter.pwn.clean", payload)
	gitTest(t, sub, "config", "filter.pwn.smudge", payload)
	gitlink := gitTest(t, sub, "rev-parse", "HEAD")
	gitTest(t, wt, "update-index", "--add", "--cacheinfo", "160000,"+gitlink+",sub")
	gitTest(t, wt, "commit", "-q", "-m", "gitlink")
	if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "feature.go"), []byte("package feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	inspection := inspectMergeTest(t, created.WorkspaceRoot, managed)
	requireMarkerAbsent(t, marker, "InspectMerge")
	request := requestFromInspection(inspection)
	request.AutoCommitDirty = true
	result, err := MergeBack(context.Background(), managed, request)
	if err != nil || !result.Merged {
		t.Fatalf("MergeBack = %+v, %v", result, err)
	}
	requireMarkerAbsent(t, marker, "MergeBack")
	tree := gitTest(t, repo, "ls-tree", "-r", result.MergedCommit)
	for _, want := range []string{"160000 commit " + gitlink + "\tsub", "\tfeature.go", "\tREADME.md"} {
		if !strings.Contains(tree, want) {
			t.Fatalf("merged tree:\n%s\nwant %q", tree, want)
		}
	}
}

// The worktree's own .git file is the agent's to write; the merge reads the
// git dir recorded when the host added the worktree.
func TestMergeBackKeepsWorktreeIdentityPinnedAtCreation(t *testing.T) {
	requirePOSIXPayload(t)
	repo := initRepo(t)
	managed := t.TempDir()
	created, err := Create(context.Background(), opened(t, repo), managed)
	if err != nil {
		t.Fatal(err)
	}
	wt := created.WorktreeRoot
	marker, payload := markerPayload(t)
	evil := filepath.Join(t.TempDir(), "evil")
	gitTest(t, repo, "clone", "-q", "--bare", repo, evil)
	cfg, err := os.OpenFile(filepath.Join(evil, "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = cfg.WriteString("[probe]\n\tx = substitute\n[filter \"pwn\"]\n\tclean = " + payload + "\n\tsmudge = " + payload + "\n")
	_ = cfg.Close()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+evil+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitTest(t, wt, "config", "--get", "probe.x"); got != "substitute" {
		t.Fatalf("discovery in the worktree read %q, want the substitute (the case being guarded)", got)
	}
	if err := os.WriteFile(filepath.Join(wt, ".gitattributes"), []byte("*.go filter=pwn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "feature.go"), []byte("package feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	inspection := inspectMergeTest(t, created.WorkspaceRoot, managed)
	if inspection.WorktreeBranch != created.Branch {
		t.Fatalf("inspection branch = %q, want %q", inspection.WorktreeBranch, created.Branch)
	}
	request := requestFromInspection(inspection)
	request.AutoCommitDirty = true
	result, err := MergeBack(context.Background(), managed, request)
	if err != nil || !result.Merged {
		t.Fatalf("MergeBack = %+v, %v", result, err)
	}
	requireMarkerAbsent(t, marker, "MergeBack")
	if _, err := os.Stat(filepath.Join(repo, "feature.go")); err != nil {
		t.Fatalf("merged file missing from source: %v", err)
	}
}
