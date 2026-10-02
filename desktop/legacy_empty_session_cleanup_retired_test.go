package main

import (
	"bytes"
	"os"
	"testing"

	"reasonix/desktop/internal/workspacestate"
)

func TestRetiredCleanupDoesNotResumeOrExpandHistoricalBatch(t *testing.T) {
	a, root := newLegacyCleanupTestApp(t)
	old, _ := createLegacyCleanupSession(t, a, root, "old-empty-candidate", false)
	if err := a.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(a.legacyCleanup.Path())
	if err != nil {
		t.Fatal(err)
	}
	newRef, _ := createLegacyCleanupSession(t, a, root, "new-formal-empty", false)
	for range 3 {
		if _, err := a.RetryLegacyEmptySessionCleanup(); err == nil {
			t.Fatal("retired RPC accepted a cleanup action")
		}
	}
	after, err := os.ReadFile(a.legacyCleanup.Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("retired API changed historical batch: %v", err)
	}
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{old.SessionID, newRef.SessionID} {
		if state.SessionStates[ref].Lifecycle == workspacestate.Archived {
			t.Fatalf("retired cleanup moved %s", ref)
		}
	}
}
