package main

import (
	"path/filepath"
	"testing"

	"reasonix/internal/gitcmd"
)

func TestWorkspaceGitCheckoutInvalidatesInFlightBranchProbe(t *testing.T) {
	base := gitScopeRepo(t)
	key := filepath.Clean(base)
	request := new(int)
	workspaceGitBranchCache.Lock()
	workspaceGitBranchCache.entries[key] = workspaceGitBranchCacheEntry{refreshing: true, request: request}
	workspaceGitBranchCache.Unlock()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	original := workspaceGitBranchForMetaProbe
	workspaceGitBranchForMetaProbe = func(gitcmd.Repo) string { close(started); <-release; return "main" }
	defer func() { workspaceGitBranchForMetaProbe = original }()
	repo := openWorkspaceRepo(base)
	go func() { refreshWorkspaceGitBranchForMeta(key, base, repo, request); close(done) }()
	<-started
	err := workspaceCheckoutBranch(repo, "shared", false)
	close(release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	workspaceGitBranchCache.Lock()
	cached, exists := workspaceGitBranchCache.entries[key]
	workspaceGitBranchCache.Unlock()
	if !exists || cached.branch != "shared" {
		t.Fatal("late pre-checkout probe replaced refreshed branch metadata")
	}
}
