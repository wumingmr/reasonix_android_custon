package sessioncatalog

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestMetadataProgressSharesWriterBoundary(t *testing.T) {
	c, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	target := DirectoryTarget{Path: t.TempDir(), Scope: "global"}
	generation, _, err := c.beginDirectoryScan(t.Context(), target, "first", 1)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.testScanProgressWriteHook = func() {
		calls++
		if c.mutationMu.TryLock() {
			c.mutationMu.Unlock()
			t.Error("scan progress bypassed the catalog writer boundary")
		}
	}
	if err := c.updateDirectoryScanProgress(t.Context(), target.Path, generation, 128); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("progress writes = %d", calls)
	}
	var count int
	if err := c.db.QueryRow(`SELECT indexed FROM catalog_directories`).Scan(&count); err != nil || count != 128 {
		t.Fatalf("progress = %d, error = %v", count, err)
	}
	// A stale worker cannot publish progress into a newer directory generation.
	if _, _, err := c.beginDirectoryScan(t.Context(), target, "second", 2); err != nil {
		t.Fatal(err)
	}
	if err := c.updateDirectoryScanProgress(t.Context(), target.Path, generation, 256); err != nil {
		t.Fatal(err)
	}
	if err := c.db.QueryRow(`SELECT indexed FROM catalog_directories`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale progress = %d, error = %v", count, err)
	}
}

func TestMetadataProgressCancellationDoesNotCompleteDiscovery(t *testing.T) {
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	target := DirectoryTarget{Path: t.TempDir(), Scope: "global"}
	generation, _, err := c.beginDirectoryScan(t.Context(), target, "first", 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	c.testScanProgressWriteHook = cancel
	if err := c.updateDirectoryScanProgress(ctx, target.Path, generation, 128); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled progress: %v", err)
	}
	if state := c.DirectoryStatus(t.Context(), target.Path); state.State != "scanning" {
		t.Fatalf("canceled progress completed discovery: %+v", state)
	}
	var count int
	if err := c.db.QueryRow(`SELECT indexed FROM catalog_directories`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("canceled progress = %d, error = %v", count, err)
	}
}
