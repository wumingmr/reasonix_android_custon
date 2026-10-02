package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteConfinesTrashAndQueryCache(t *testing.T) {
	for _, directory := range []string{".trash", ".query-cache"} {
		t.Run(directory, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			persistence := NewFilesystemPersistence(root)
			created, err := persistence.Create(CreateOptions{SessionID: "owned"})
			if err != nil {
				t.Fatal(err)
			}
			if err := created.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(outside, "owned"), 0o700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(outside, "owned", "keep")
			if err := os.WriteFile(sentinel, []byte("outside data"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, directory)); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			err = persistence.Delete(t.Context(), "owned")
			if directory == ".trash" {
				if err == nil {
					t.Fatal("delete moved the session outside the root")
				}
				if _, err := os.Stat(filepath.Join(root, "owned", "manifest.json")); err != nil {
					t.Fatalf("failed delete removed the session: %v", err)
				}
			} else if err != nil {
				t.Fatalf("delete with an unavailable advisory cache: %v", err)
			}
			body, err := os.ReadFile(sentinel)
			if err != nil || string(body) != "outside data" {
				t.Fatalf("delete changed external cache data: %q, %v", body, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 || entries[0].Name() != "owned" {
				t.Fatalf("delete wrote an external tombstone: %v, %v", entries, err)
			}
		})
	}
}
