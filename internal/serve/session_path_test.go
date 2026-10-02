package serve

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
)

func TestResolveSessionPathRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unprivileged Windows CI cannot create the symlink fixture")
	}
	sessionDir := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.jsonl")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(sessionDir, "alias.jsonl")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}

	ctrl := control.New(control.Options{SessionDir: sessionDir})
	server := New(ctrl, NewBroadcaster(), config.ServeConfig{})
	t.Cleanup(server.Close)
	if _, err := server.resolveSessionPath(alias); err == nil || err.Error() != "path outside session dir" {
		t.Fatalf("resolve symlink escape error = %v, want path outside session dir", err)
	}
}
