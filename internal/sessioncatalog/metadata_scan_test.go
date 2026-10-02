package sessioncatalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
)

func TestMetadataDiscoveryNeverRepairsOrClassifiesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recovery.jsonl")
	body := []byte("deliberately invalid authoritative content\n")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(path, agent.BranchMeta{Recovered: true, ParentID: "parent", TopicID: "recovered-topic", Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	c.testSessionContentLoadHook = func(string) { t.Error("metadata discovery read content") }
	if !c.opts.DisableRepair {
		t.Fatal("metadata mode admitted background content repair")
	}
	if err := c.ReconcileDirectory(t.Context(), DirectoryTarget{Path: dir, Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	record, found, err := c.GetSession(t.Context(), path)
	if err != nil || !found || !record.OrdinaryVisible || record.TurnsState != TurnsUnknown || record.RecoveryCopy {
		t.Fatalf("unverified recovery must remain reachable: %+v %v %v", record, found, err)
	}
	if err := c.IndexSessionPath(t.Context(), DirectoryTarget{Path: dir, Scope: "global"}, path); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(body) {
		t.Fatal("metadata discovery changed authoritative content")
	}
	if !c.DirectoryScanReady(t.Context(), dir) {
		t.Fatal("unknown content count prevented directory readiness")
	}
}

func TestMetadataDiscoveryOversizedSidecarStillVisible(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.jsonl")
	if err := os.WriteFile(path, []byte("invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent.BranchMetaPath(path), []byte(strings.Repeat("x", 70<<10)), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	if err := c.ReconcileDirectory(t.Context(), DirectoryTarget{Path: dir, Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	page, err := c.ListTopics(t.Context(), TopicPageRequest{Scope: "global", Limit: 50})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("metadata fallback disappeared: %+v %v", page, err)
	}
}

func TestMetadataDiscoveryCancellationCannotMarkRowsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.jsonl")
	if err := os.WriteFile(path, []byte("invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	target := DirectoryTarget{Path: dir, Scope: "global"}
	if err := c.ReconcileDirectory(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.ReconcileDirectory(ctx, target); err == nil {
		t.Fatal("cancelled scan succeeded")
	}
	row, found, err := c.GetSession(t.Context(), path)
	if err != nil || !found || row.MissingSince != 0 {
		t.Fatalf("cancelled scan marked missing: %+v %v", row, err)
	}
}

func TestMetadataDiscoveryMissingUnusedRootIsReady(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	target := DirectoryTarget{Path: dir, Scope: "global"}
	if err := c.ReconcileDirectory(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if !c.DirectoryScanReady(t.Context(), dir) || c.Status().State == StateDegraded {
		t.Fatalf("unused root is not empty and ready: %+v", c.Status())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("discovery created the optional root")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "later.jsonl")
	if err := os.WriteFile(path, []byte("unreadable body\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.ReconcileDirectory(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if _, found, err := c.GetSession(t.Context(), path); err != nil || !found {
		t.Fatalf("later source was not discovered: %v", err)
	}
	if err := os.Rename(dir, dir+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := c.ReconcileDirectory(t.Context(), target); !os.IsNotExist(err) {
		t.Fatalf("known root disappearance accepted: %v", err)
	}
	row, found, err := c.GetSession(t.Context(), path)
	if err != nil || !found || row.MissingSince != 0 || c.DirectoryScanReady(t.Context(), dir) {
		t.Fatalf("unavailable root hid history: %+v %v", row, err)
	}
}

func TestMetadataDiscoveryEmptyRootCannotHideConcurrentSave(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new-root")
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	target := DirectoryTarget{Path: dir, Scope: "global"}
	scan, err := c.startMetadataScan(t.Context(), target, c.mutationSeq.Add(1), true)
	if err != nil {
		t.Fatal(err)
	}
	defer scan.close(t.Context(), nil)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "saved.jsonl")
	if err := os.WriteFile(path, []byte("unread body\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.IndexSessionPath(t.Context(), target, path); err != nil {
		t.Fatal(err)
	}
	if done, _, err := scan.step(t.Context()); err != nil || !done {
		t.Fatalf("empty snapshot did not finish: %v %v", done, err)
	}
	row, found, err := c.GetSession(t.Context(), path)
	if err != nil || !found || row.MissingSince != 0 {
		t.Fatalf("older empty discovery hid the save: %+v %v", row, err)
	}
}
