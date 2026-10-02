package historycatalog

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

func TestCaptureSearchKeepsRankingAcrossUnrelatedWrites(t *testing.T) {
	root := t.TempDir()
	messages := make([]provider.Message, 405)
	for i := range messages {
		messages[i] = provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("snapshot marker %d", i)}
	}
	path := filepath.Join(root, "source.jsonl")
	saveMessages(t, path, messages...)
	c, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "history.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	target := Root{Path: root, Scope: "project", WorkspaceRoot: root}
	if err := c.ReconcileRoot(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	err = c.CaptureSearch(t.Context(), SearchRequest{Query: "snapshot marker", SessionPath: path}, func(candidate Candidate) error {
		if seen[candidate.MessageIndex] {
			t.Fatalf("duplicate %d", candidate.MessageIndex)
		}
		seen[candidate.MessageIndex] = true
		if len(seen) == 200 {
			other := t.TempDir()
			saveMessages(t, filepath.Join(other, "other.jsonl"), provider.Message{Role: provider.RoleUser, Content: "snapshot snapshot snapshot marker marker"})
			return c.ReconcileRoot(t.Context(), Root{Path: other, Scope: "global"})
		}
		return nil
	})
	if err != nil || len(seen) != 405 {
		t.Fatalf("capture rows=%d err=%v", len(seen), err)
	}
}
