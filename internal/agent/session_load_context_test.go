package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestLoadSessionContextCancelsBeforeSaveLockAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.jsonl")
	unlock := lockSessionSavePath(path)
	defer unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := LoadSessionContext(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lock admission: %v", err)
	}
}
