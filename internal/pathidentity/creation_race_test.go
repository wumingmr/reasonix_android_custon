package pathidentity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAllowsCreationBetweenFilesystemObservations(t *testing.T) {
	for _, tail := range []string{"state.lock", filepath.Join("new", "state.lock")} {
		t.Run(tail, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, tail)
			created := false
			got, err := resolveThroughExistingAncestorWith(path, func(current string) (string, error) {
				resolved, resolveErr := filepath.EvalSymlinks(current)
				if !created {
					created = true
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return resolved, resolveErr
			})
			if err != nil {
				t.Fatalf("concurrent creation rejected: %v", err)
			}
			want, err := filepath.EvalSymlinks(path)
			if err != nil || !created || got != want {
				t.Fatalf("resolved = %q, want %q; created=%v, err=%v", got, want, created, err)
			}
		})
	}
}

func TestResolveRejectsDanglingAncestorAndLeaf(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "absent"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for _, path := range []string{link, filepath.Join(link, "state.lock")} {
		_, err := Resolve(path, Options{FollowLeaf: true})
		var identityErr *Error
		if !errors.As(err, &identityErr) || identityErr.Kind != ErrorUnavailable {
			t.Fatalf("Resolve(%q) = %v, want unavailable", path, err)
		}
	}
}
