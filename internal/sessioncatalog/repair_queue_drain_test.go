package sessioncatalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"reasonix/internal/agent"
)

// The wake channel coalesces on one key and is sized by QueueCapacity, so a
// capacity far below the pending set must not strand rows: one wake has to
// drain every due row. Repair itself is stubbed because the filesystem path
// defers a transient failure by repairBackoff's 30s floor, which would decide
// this test's outcome for a reason that has nothing to do with queue capacity.
func TestRepairDrainEventuallyCompletesBeyondQueue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "catalog.sqlite")
	seed, err := Open(ctx, Options{Path: path, DisableRepair: true})
	if err != nil {
		t.Fatal(err)
	}
	const total = 8
	for i := range total {
		session := filepath.Join(dir, fmt.Sprintf("%02d.jsonl", i))
		if err := os.WriteFile(session, []byte(`{"role":"user","content":"turn"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := seed.UpsertSession(ctx, SessionRecord{
			Path: session, Directory: dir, Scope: "global", TopicID: fmt.Sprintf("t%d", i),
			TurnsState: TurnsUnknown, Health: HealthOK, LastActivityAt: int64(i + 1),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	repaired := map[string]struct{}{}
	drained := make(chan struct{})
	catalog, err := Open(ctx, Options{
		Path: path, QueueCapacity: 2, Now: time.Now,
		repairSession: func(_ context.Context, session string) (agent.SessionListingRepairResult, error) {
			mu.Lock()
			defer mu.Unlock()
			if _, seen := repaired[session]; !seen {
				repaired[session] = struct{}{}
				if len(repaired) == total {
					close(drained)
				}
			}
			return agent.SessionListingRepairResult{Status: agent.SessionListingRepairApplied, Preview: "ok", Turns: 1}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })

	select {
	case <-drained:
	case <-time.After(30 * time.Second):
		mu.Lock()
		seen := len(repaired)
		mu.Unlock()
		t.Fatalf("repaired %d of %d sessions; a queue of 2 stranded the rest", seen, total)
	}
	// Every row reached repair; the batch commit that clears the pending count
	// lands just after the last call, so wait for the count the drain owes.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if catalog.Status().RepairPending == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("repair pending stuck at %d after every session was repaired", catalog.Status().RepairPending)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
