package control

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"reasonix/internal/session"
)

func TestFailedSessionRebindReleasesPreviousWriter(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "old.jsonl")
	c := newOwnedTestController(t, Options{SessionPath: oldPath})
	if c.sessionEventStore() == nil {
		t.Fatal("initial writer not opened")
	}
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	c.rebindTurnEvents(filepath.Join(blocked, "new.jsonl"))
	if c.sessionEventStore() != nil {
		t.Fatal("failed rebind retained an execution store")
	}
	reopened, err := session.Open(sessionDirectory(oldPath), "old")
	if err != nil {
		t.Fatalf("failed rebind leaked old writer: %v", err)
	}
	if err := reopened.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsSessionDirectoryIgnoresPathCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path identity")
	}
	path := filepath.Join(t.TempDir(), "active.jsonl")
	if sessionDirectory(path) != sessionDirectory(strings.ToLower(path)) {
		t.Fatal("case variants select different shared writer identities")
	}
}
