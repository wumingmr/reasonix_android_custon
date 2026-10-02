package boot

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"reasonix/internal/gitcmd"
)

// workspaceRepoTimeout bounds the one git call a session open makes.
const workspaceRepoTimeout = 10 * time.Second

// workspaceRepo is root's git identity for the session being built, settled
// before the session runs any command. A rebuild of the same workspace keeps
// the identity its session opened with instead of rediscovering it from files
// that session could since have written.
func workspaceRepo(ctx context.Context, carried gitcmd.Repo, root string) gitcmd.Repo {
	abs, err := filepath.Abs(root)
	if err != nil {
		return gitcmd.Repo{Dir: root}
	}
	if carried.Dir != "" && filepath.Clean(carried.Dir) == abs {
		return carried
	}
	if !underGitMarker(abs) {
		return gitcmd.Repo{Dir: abs}
	}
	ctx, cancel := context.WithTimeout(ctx, workspaceRepoTimeout)
	defer cancel()
	repo, err := gitcmd.Open(ctx, abs)
	if err != nil {
		return gitcmd.Repo{Dir: abs}
	}
	return repo
}

// underGitMarker reports whether dir or an ancestor holds a .git entry, so a
// workspace outside any repository never starts git (on macOS a missing git is
// an installer prompt, not an error).
func underGitMarker(dir string) bool {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}
