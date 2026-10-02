package sessioncatalog

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func TestReadViewFreezesCrossPageOrder(t *testing.T) {
	c, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), DisableRepair: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	for i := range 405 {
		if err := c.UpsertSession(t.Context(), SessionRecord{Path: fmt.Sprintf("/sessions/%03d.jsonl", i), Directory: "/sessions", Scope: "global", TopicID: "shared", LastActivityAt: int64(i), Health: HealthOK, TurnsState: TurnsValid}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	err = c.WithReadView(t.Context(), func(ctx context.Context) error {
		topics, err := c.ListTopics(ctx, TopicPageRequest{Scope: "global", Limit: 1})
		if err != nil {
			return err
		}
		if len(topics.Items) != 1 || len(topics.Items[0].Sessions) != 405 {
			t.Fatalf("topic hydration truncated siblings: topics=%d", len(topics.Items))
		}
		cursor := ""
		for {
			page, err := c.ListSessions(ctx, SessionPageRequest{Scope: "global", Cursor: cursor, Limit: 200})
			if err != nil {
				return err
			}
			if page.StaleCursor {
				t.Fatal("writer invalidated fixed read view")
			}
			for _, row := range page.Items {
				if seen[row.Path] {
					t.Fatalf("duplicate %s", row.Path)
				}
				seen[row.Path] = true
			}
			if cursor == "" {
				if err := c.UpsertSession(t.Context(), SessionRecord{Path: "/sessions/000.jsonl", Directory: "/sessions", Scope: "global", TopicID: "shared", LastActivityAt: 999, Health: HealthOK, TurnsState: TurnsValid}); err != nil {
					return err
				}
			}
			if page.NextCursor == "" {
				return nil
			}
			cursor = page.NextCursor
		}
	})
	if err != nil || len(seen) != 405 {
		t.Fatalf("rows=%d err=%v", len(seen), err)
	}
}
