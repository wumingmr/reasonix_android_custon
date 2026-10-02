package boot

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveWorkspaceRootExplicitAndGitFallback proves the --dir contract:
// an explicit workspace root is honored even inside a git repository, while an
// empty root still falls back to the nearest git root from the working directory.
func TestResolveWorkspaceRootExplicitAndGitFallback(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Explicit --dir pins the workspace root, not the git root of the repo.
	if got := ResolveWorkspaceRoot(sub); got != sub {
		t.Fatalf("ResolveWorkspaceRoot(%q) = %q, want the explicit dir", sub, got)
	}

	// No explicit root: fall back to the nearest git root from the CWD.
	t.Chdir(sub)
	got := ResolveWorkspaceRoot("")
	wantRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("ResolveWorkspaceRoot(%q) returned unusable path %q: %v", "", got, err)
	}
	if gotResolved != wantRoot {
		t.Fatalf("ResolveWorkspaceRoot(\"\") = %q, want git root %q", got, repo)
	}
}
