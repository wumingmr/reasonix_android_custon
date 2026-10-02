package sessioncatalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Measures metadata/SQLite work without the scheduler's deliberate inter-slice
// sleep. Native startup acceptance remains a separate production-package test.
func BenchmarkMetadataScanUnchanged10K(b *testing.B) {
	dir := b.TempDir()
	for i := range 10000 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%05d.jsonl", i)), []byte("unread body\n"), 0600); err != nil {
			b.Fatal(err)
		}
	}
	c, err := Open(b.Context(), Options{Path: filepath.Join(b.TempDir(), "catalog.sqlite"), MetadataOnly: true, StartPaused: true})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = c.Close(context.Background()) })
	target := DirectoryTarget{Path: dir, Scope: "global"}
	scan := func() {
		run, err := c.startMetadataScan(b.Context(), target, c.mutationSeq.Add(1), false)
		if err != nil {
			b.Fatal(err)
		}
		defer func() { run.close(b.Context(), err) }()
		for {
			var done bool
			done, _, err = run.step(b.Context())
			if err != nil {
				b.Fatal(err)
			}
			if done {
				return
			}
		}
	}
	started := time.Now()
	scan()
	cold := time.Since(started)
	b.ResetTimer()
	for range b.N {
		scan()
	}
	b.ReportMetric(float64(cold)/float64(time.Millisecond), "cold-ms")
}
