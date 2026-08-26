package sessioncatalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/agent"
)

func TestIndexSessionPathSkipsUnchangedProjection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(path, agent.BranchMeta{
		Scope: "global", TopicID: "topic", TopicTitle: "Chat",
		SchemaVersion: agent.BranchMetaCountsVersion, Turns: 1,
	}); err != nil {
		t.Fatal(err)
	}
	events := 0
	catalog, err := Open(ctx, Options{InMemory: true, DisableRepair: true, OnRevision: func(uint64, []string, string) { events++ }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close(ctx) })
	target := DirectoryTarget{Path: dir, Scope: "global"}
	if err := catalog.ReconcileDirectory(ctx, target); err != nil {
		t.Fatal(err)
	}
	revision := catalog.Status().Revision
	before, _, _ := catalog.GetSession(ctx, path)
	for range 5 {
		if err := catalog.IndexSessionPath(ctx, target, path); err != nil {
			t.Fatal(err)
		}
	}
	if got := catalog.Status().Revision; got != revision {
		after, _, _ := catalog.GetSession(ctx, path)
		t.Logf("before=%+v after=%+v", before, after)
		t.Fatalf("revision after unchanged exact indexes = %d, want %d", got, revision)
	}
	if events != 1 {
		t.Fatalf("revision events after unchanged exact indexes = %d, want 1", events)
	}
}

func TestIndexSessionPathDoesNotRequeueRepairAfterRepair(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(path, agent.BranchMeta{TopicID: "topic", SchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	catalog, err := Open(ctx, Options{InMemory: true, QueueCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close(ctx) })
	target := DirectoryTarget{Path: dir, Scope: "global"}
	if err := catalog.ReconcileDirectory(ctx, target); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if catalog.Status().RepairPending == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if catalog.Status().RepairPending != 0 {
		t.Fatal("repair did not complete")
	}
	for range 20 {
		if err := catalog.IndexSessionPath(ctx, target, path); err != nil {
			t.Fatal(err)
		}
	}
	if got := catalog.Status().RepairPending; got != 0 {
		t.Fatalf("repair pending after unchanged exact indexes = %d", got)
	}
}

func TestExactIndexDoesNotDowngradeKnownCounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	catalog, err := Open(ctx, Options{InMemory: true, DisableRepair: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close(ctx) })
	record := SessionRecord{Path: "/sessions/chat.jsonl", Directory: "/sessions", Scope: "global", TopicID: "topic", CreatedAt: 1, LastActivityAt: 2, Preview: "hi", Turns: 1, TurnsState: TurnsValid, ContentFingerprint: "10:1", MetaFingerprint: "20:1", Health: HealthOK}
	if err := catalog.UpsertSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.Preview, record.Turns, record.TurnsState, record.MetaFingerprint = "", 0, TurnsUnknown, "20:2"
	record.Recovered, record.RecoveryReason, record.RecoveryDigest, record.ParentID = true, "recovery", "digest", "parent"
	if err := catalog.UpsertSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, ok, err := catalog.GetSession(ctx, record.Path)
	if err != nil || !ok {
		t.Fatalf("GetSession: ok=%v err=%v", ok, err)
	}
	if got.TurnsState != TurnsValid || got.Turns != 1 || got.Preview != "hi" ||
		!got.Recovered || got.RecoveryReason != "recovery" || got.RecoveryDigest != "digest" || got.ParentID != "parent" {
		t.Fatalf("exact index lost known counts or recovery metadata: %+v", got)
	}
}

func TestExactIndexDoesNotRegressKnownActivity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	current := created.Add(15 * 24 * time.Hour)
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
		CreatedAt: created, UpdatedAt: current, Scope: "global", TopicID: "topic",
		TopicTitle: "Chat", SchemaVersion: agent.BranchMetaCountsVersion, Turns: 1,
	}); err != nil {
		t.Fatal(err)
	}
	catalog, err := Open(ctx, Options{InMemory: true, DisableRepair: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close(ctx) })
	target := DirectoryTarget{Path: dir, Scope: "global"}
	if err := catalog.ReconcileDirectory(ctx, target); err != nil {
		t.Fatal(err)
	}
	// A transient sidecar read can expose an older timestamp while the
	// authoritative transcript is unchanged. Exact indexing must not move the
	// conversation backwards in the sidebar.
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
		CreatedAt: created, UpdatedAt: created.Add(30 * time.Hour), Scope: "global", TopicID: "topic",
		TopicTitle: "Chat", SchemaVersion: agent.BranchMetaCountsVersion, Turns: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.IndexSessionPath(ctx, target, path); err != nil {
		t.Fatal(err)
	}
	record, ok, err := catalog.GetSession(ctx, path)
	if err != nil || !ok {
		t.Fatalf("GetSession: ok=%v err=%v", ok, err)
	}
	if record.LastActivityAt != current.UnixMilli() {
		t.Fatalf("lastActivityAt = %d, want preserved %d", record.LastActivityAt, current.UnixMilli())
	}
}
