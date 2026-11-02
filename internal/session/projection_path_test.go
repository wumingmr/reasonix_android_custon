package session

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectionPathsRejectInvalidSessionIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "session storage #% 会话")
	for name, pathFor := range map[string]func(string, string) string{
		"history": historyIndexPath, "search": searchIndexPath,
	} {
		t.Run(name, func(t *testing.T) {
			for _, id := range []string{"", ".", "..", "../other", "nested/id", `nested\id`, "/outside", `C:\outside`, "CON", ".hidden", "id.", "bad\x00id", strings.Repeat("x", 256)} {
				if path := pathFor(root, id); path != "" {
					t.Errorf("invalid session identity %q acquired projection path %q", id, path)
				}
			}
			for _, id := range []string{"session-123", "turn.1", "会话", "space in id"} {
				path := pathFor(root, id)
				wantDir := filepath.Join(root, ".query-cache", id)
				if path == "" || filepath.Dir(path) != wantDir {
					t.Errorf("valid session identity %q: path=%q want directory=%q", id, path, wantDir)
				}
			}
		})
	}
}
