package builtin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/tool"
)

func TestObservedDeletionCannotBecomeBlindCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := observedContext()
	if _, err := (readFile{}).Execute(ctx, argsJSON(t, map[string]any{"path": path})); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, err := (writeFile{}).Execute(ctx, argsJSON(t, map[string]any{"path": path, "content": "new"}))
	if !errors.Is(err, ErrFileChanged) {
		t.Fatalf("deleted observed source was recreated: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("write changed absent target: %v", err)
	}
}

func TestDiskWriteDoesNotSwitchToUnobservedOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := observedContext()
	ov := &fakeOverlay{files: map[string]string{}, writes: map[string]string{}}
	if _, err := (readFile{overlay: ov}).Execute(ctx, argsJSON(t, map[string]any{"path": path})); err != nil {
		t.Fatal(err)
	}
	if _, err := (writeFile{overlay: ov}).Execute(ctx, argsJSON(t, map[string]any{"path": path, "content": "new"})); err != nil {
		t.Fatal(err)
	}
	if len(ov.writes) != 0 {
		t.Fatal("disk observation authorized an overlay write")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new" {
		t.Fatalf("disk write = %q, %v", got, err)
	}
}

func TestSameContentReplacementBeforePublishIsStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := observedContext()
	if _, err := (readFile{}).Execute(ctx, argsJSON(t, map[string]any{"path": path})); err != nil {
		t.Fatal(err)
	}
	ctx = tool.WithWriteIntentHook(ctx, func(tool.FileWriteIntent) error {
		// Keep the old inode allocated so identity reuse cannot hide replacement.
		if err := os.Rename(path, path+".old"); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("old"), 0o600)
	})
	_, err := (editFile{}).Execute(ctx, argsJSON(t, map[string]any{"path": path, "old_string": "old", "new_string": "new"}))
	if !errors.Is(err, ErrFileChanged) {
		t.Fatalf("replacement accepted: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old" {
		t.Fatalf("external replacement overwritten: %q", got)
	}
}

func TestMovePublicationCannotOverwriteConcurrentCreator(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := renameFile
	renameFile = func(from, to string) error {
		if err := os.WriteFile(to, []byte("winner"), 0o600); err != nil {
			return err
		}
		return old(from, to)
	}
	t.Cleanup(func() { renameFile = old })
	_, err := (moveFile{}).Execute(context.Background(), argsJSON(t, map[string]any{"source_path": src, "destination_path": dst}))
	if err == nil {
		t.Fatal("move overwrote concurrent destination")
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "winner" {
		t.Fatalf("destination = %q", got)
	}
	got, _ = os.ReadFile(src)
	if string(got) != "source" {
		t.Fatalf("source = %q", got)
	}
}
