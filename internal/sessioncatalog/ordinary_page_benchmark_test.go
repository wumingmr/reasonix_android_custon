package sessioncatalog

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// This measures only the persisted metadata page, not native application
// startup or execution readiness. Run with -benchtime=30x for a fixed sample.
func BenchmarkOrdinaryFirstPage(b *testing.B) {
	for _, count := range []int{100, 10000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			c, err := Open(b.Context(), Options{Path: filepath.Join(b.TempDir(), "catalog.sqlite"), MetadataOnly: true, StartPaused: true})
			if err != nil {
				b.Fatal(err)
			}
			defer c.Close(context.Background())
			// Metadata-only fixture: no transcript can be opened accidentally.
			_, err = c.db.ExecContext(b.Context(), `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
			INSERT INTO catalog_sessions(path,path_key,directory,directory_key,scope,workspace_root,workspace_root_key,topic_id,topic_title,created_at,last_activity_at,ordinary_visible,logical_topic_id)
			SELECT printf('/history/%08d.jsonl',i),printf('/history/%08d.jsonl',i),'/history','/history','global','','',printf('topic-%08d',i),'History',i,i,1,printf('topic-%08d',i) FROM n`, count)
			if err != nil {
				b.Fatal(err)
			}
			_, err = c.db.ExecContext(b.Context(), `INSERT INTO catalog_topics(scope,workspace_root,workspace_root_key,topic_id,title,created_at,last_activity_at) SELECT scope,workspace_root,workspace_root_key,topic_id,topic_title,created_at,last_activity_at FROM catalog_sessions`)
			if err != nil {
				b.Fatal(err)
			}
			samples := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for range b.N {
				start := time.Now()
				lease, err := c.OpenReadLease(b.Context())
				if err != nil {
					b.Fatal(err)
				}
				rows, err := c.ListOrdinarySessions(lease.Context(b.Context()), OrdinaryPageRequest{Scope: "global", Limit: 50})
				lease.Close()
				if err != nil || len(rows) != 50 {
					b.Fatalf("first page count=%d error=%v", len(rows), err)
				}
				samples = append(samples, time.Since(start))
			}
			b.StopTimer()
			slices.Sort(samples)
			if len(samples) > 0 {
				b.ReportMetric(float64(samples[(len(samples)*95+99)/100-1])/float64(time.Millisecond), "p95-ms")
			}
		})
	}
}
