package main

import (
	"context"
	"path/filepath"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/gitcmd"
)

const workspaceRepoOpenLimit = 3 * time.Second

// sessionWorkspaceRepo is the git identity ctrl's session resolved for base
// when it opened, and whether that session holds one for base at all.
func sessionWorkspaceRepo(ctrl control.SessionAPI, base string) (gitcmd.Repo, bool) {
	c, ok := ctrl.(interface{ WorkspaceRepo() gitcmd.Repo })
	if !ok {
		return gitcmd.Repo{}, false
	}
	repo := c.WorkspaceRepo()
	return repo, repo.Dir != "" && filepath.Clean(repo.Dir) == filepath.Clean(base)
}

// workspaceRepo is the identity host git reads root through: the one its
// session opened with, or, for a root no session holds yet, the one it
// resolves to now, as opening it would.
func workspaceRepo(root string, ctrl control.SessionAPI) (gitcmd.Repo, error) {
	base, err := workspaceBaseFromRoot(root)
	if err != nil {
		return gitcmd.Repo{}, err
	}
	if repo, ok := sessionWorkspaceRepo(ctrl, base); ok {
		return repo, nil
	}
	return openWorkspaceRepo(base), nil
}

func openWorkspaceRepo(base string) gitcmd.Repo {
	ctx, cancel := context.WithTimeout(context.Background(), workspaceRepoOpenLimit)
	defer cancel()
	repo, err := gitcmd.Open(ctx, base)
	if err != nil {
		return gitcmd.Repo{Dir: base}
	}
	return repo
}

// workspaceRepoForRoot is root's identity as a tab session holding it opened
// it, or as opening it now would.
func (a *App) workspaceRepoForRoot(root string) (gitcmd.Repo, error) {
	var ctrl control.SessionAPI
	a.mu.RLock()
	for _, tab := range a.tabs {
		if tab.Ctrl != nil && sameProjectRoot(tab.WorkspaceRoot, root) {
			ctrl = tab.Ctrl
			break
		}
	}
	a.mu.RUnlock()
	return workspaceRepo(root, ctrl)
}
