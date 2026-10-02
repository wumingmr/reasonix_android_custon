package sessioncatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

// Measure the page users first see when recently indexed history includes
// deleted topics. The filter must stay bounded by the requested page and the
// tombstones, not the full retained library.
func BenchmarkOrdinaryFirstPageWithDeletedTopics(b *testing.B) {
	const sessions = 100000
	for _, deletedCount := range []int{0, 100, 1000, 10000} {
		b.Run(fmt.Sprint(deletedCount), func(b *testing.B) {
			catalog, err := Open(b.Context(), Options{Path: filepath.Join(b.TempDir(), "catalog.sqlite"), MetadataOnly: true, StartPaused: true})
			if err != nil {
				b.Fatal(err)
			}
			defer catalog.Close(context.Background())
			_, err = catalog.db.ExecContext(b.Context(), `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
				INSERT INTO catalog_sessions(path,path_key,directory,directory_key,scope,workspace_root,workspace_root_key,topic_id,topic_title,created_at,last_activity_at,ordinary_visible,logical_topic_id)
				SELECT printf('/history/%08d.jsonl',i),printf('/history/%08d.jsonl',i),'/history','/history','global','','',printf('topic-%08d',i),'History',i,i,1,printf('topic-%08d',i) FROM n`, sessions)
			if err != nil {
				b.Fatal(err)
			}
			_, err = catalog.db.ExecContext(b.Context(), `INSERT INTO catalog_topics(scope,workspace_root,workspace_root_key,topic_id,title,created_at,last_activity_at)
				SELECT scope,workspace_root,workspace_root_key,topic_id,topic_title,created_at,last_activity_at FROM catalog_sessions`)
			if err != nil {
				b.Fatal(err)
			}
			deleted := make([]string, 0, deletedCount)
			for i := sessions; i > sessions-deletedCount; i-- {
				deleted = append(deleted, fmt.Sprintf("topic-%08d", i))
			}
			encoded := ""
			if len(deleted) > 0 {
				body, err := json.Marshal(deleted)
				if err != nil {
					b.Fatal(err)
				}
				encoded = string(body)
			}
			b.ResetTimer()
			for range b.N {
				lease, err := catalog.OpenReadLease(b.Context())
				if err != nil {
					b.Fatal(err)
				}
				rows, err := catalog.ListOrdinarySessions(lease.Context(b.Context()), OrdinaryPageRequest{Scope: "global", Limit: 50, ExcludedTopicIDsJSON: encoded})
				lease.Close()
				if err != nil || len(rows) != 50 {
					b.Fatalf("first page count=%d error=%v", len(rows), err)
				}
			}
		})
	}
}
