package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/gitcmd"
)

func (a *App) gitWorkspaceRepoForTab(tabID, expectedRoot string) (gitcmd.Repo, error) {
	tabID = strings.TrimSpace(tabID)
	if tabID == "" {
		return gitcmd.Repo{}, fmt.Errorf("workspace tab is required")
	}
	root, ctrl, ok := a.workspaceChangesTarget(tabID)
	if !ok || !filepath.IsAbs(root) {
		return gitcmd.Repo{}, fmt.Errorf("workspace tab %q is unavailable", tabID)
	}
	if !filepath.IsAbs(expectedRoot) || !sameProjectRoot(root, expectedRoot) {
		return gitcmd.Repo{}, fmt.Errorf("workspace tab %q has changed project", tabID)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return gitcmd.Repo{}, fmt.Errorf("workspace tab %q directory is unavailable", tabID)
	}
	return workspaceRepo(root, ctrl)
}

func (a *App) GitBranchesForTab(tabID, workspaceRoot string) ([]string, error) {
	repo, err := a.gitWorkspaceRepoForTab(tabID, workspaceRoot)
	if err != nil {
		return nil, err
	}
	return workspaceLocalBranches(repo)
}

func (a *App) GitCheckoutForTab(tabID, workspaceRoot, branch string) error {
	repo, err := a.gitWorkspaceRepoForTab(tabID, workspaceRoot)
	if err != nil {
		return err
	}
	return workspaceCheckoutBranch(repo, branch, false)
}

func (a *App) GitCreateBranchForTab(tabID, workspaceRoot, name string) error {
	repo, err := a.gitWorkspaceRepoForTab(tabID, workspaceRoot)
	if err != nil {
		return err
	}
	return workspaceCheckoutBranch(repo, name, true)
}

// The launcher needs only Git totals, not session checkpoints or per-file views.
func (a *App) WorkspaceGitStatsForTab(tabID, workspaceRoot string) (WorkspaceChangesView, error) {
	repo, err := a.gitWorkspaceRepoForTab(tabID, workspaceRoot)
	if err != nil {
		return WorkspaceChangesView{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out := WorkspaceChangesView{Files: []WorkspaceChangeView{}, GitAvailable: true}
	out.GitBranch, _ = workspaceGitBranchContext(ctx, repo)
	entries, err := workspaceGitStatusContext(ctx, repo)
	if err != nil {
		out.GitAvailable, out.Incomplete, out.GitErr = false, true, err.Error()
		return out, nil
	}
	untracked := []string{}
	for _, entry := range entries {
		if entry.Status == "??" {
			untracked = append(untracked, entry.Path)
		}
	}
	out.Added, out.Removed, out.Incomplete = workspaceGitDiffTally(ctx, repo, untracked)
	return out, nil
}

func workspaceLocalBranches(repo gitcmd.Repo) ([]string, error) {
	raw, err := workspaceGitOutputWithTimeout(3*time.Second, repo, "-C", repo.Dir, "branch", "--format=%(refname:short)")
	if err != nil {
		return nil, err
	}
	return append([]string{}, strings.FieldsFunc(strings.TrimSpace(string(raw)), func(r rune) bool { return r == '\n' })...), nil
}

// workspaceCheckoutBranch switches repo's branch. Submodules are not updated
// (gitcmd pins submodule.recurse off for checkout).
func workspaceCheckoutBranch(repo gitcmd.Repo, name string, create bool) error {
	base := repo.Dir
	name = strings.TrimSpace(name)
	if !validGitBranchName(name) {
		return fmt.Errorf("invalid branch name %q", name)
	}
	args := []string{"-C", base, "checkout"}
	if create {
		args = append(args, "-b")
	}
	args = append(args, name)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := workspaceGitCommand(ctx, repo, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git checkout: %w: %s", err, strings.TrimSpace(string(out)))
	}
	branch, _ := workspaceGitBranchContext(ctx, repo)
	workspaceGitBranchCache.Lock()
	if branch == "" {
		delete(workspaceGitBranchCache.entries, filepath.Clean(base))
	} else {
		workspaceGitBranchCache.entries[filepath.Clean(base)] = workspaceGitBranchCacheEntry{
			branch: branch, expires: time.Now().Add(workspaceGitBranchCacheTTL),
		}
	}
	workspaceGitBranchCache.Unlock()
	return nil
}
