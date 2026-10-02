package pathidentity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatcherResolvesEachDistinctPathOnce(t *testing.T) {
	base := t.TempDir()
	dirs := []string{}
	for _, name := range []string{"a", "b", "c", "d"} {
		dir := filepath.Join(base, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, dir)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(dirs[2], alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	dirs = append(dirs, alias, filepath.Join(base, "missing"))
	calls := map[string]int{}
	matcher := NewMatcher(Options{FollowLeaf: true}, func(path string, options Options) (Identity, error) {
		calls[path]++
		return Resolve(path, options)
	})
	for _, left := range dirs {
		for _, right := range dirs {
			got, gotErr := matcher.Same(left, right)
			want, wantErr := Same(left, right, Options{FollowLeaf: true})
			if got != want || (gotErr == nil) != (wantErr == nil) {
				t.Fatalf("Same(%s, %s) = %v, %v; want %v, %v", left, right, got, gotErr, want, wantErr)
			}
		}
	}
	for path, n := range calls {
		if n != 1 {
			t.Errorf("%s resolved %d times in one pass", path, n)
		}
	}
	if len(calls) != len(dirs) {
		t.Errorf("resolved %d paths, want %d", len(calls), len(dirs))
	}
}
