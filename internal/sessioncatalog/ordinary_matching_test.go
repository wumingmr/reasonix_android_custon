package sessioncatalog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchingOrdinaryPagesKeepSnapshotAndCancelBetweenCandidates(t *testing.T) {
	c, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	root := t.TempDir()
	for i := range 137 {
		title := "irrelevant"
		if i == 1 || i == 80 || i == 135 {
			title = "ÜBER 历史"
		}
		err := c.UpsertSession(t.Context(), SessionRecord{Path: filepath.Join(root, fmt.Sprintf("%03d.jsonl", i)), Directory: root,
			Scope: "global", TopicID: fmt.Sprint(i), TopicTitle: title, LastActivityAt: int64(i + 1), OrdinaryVisible: true, Health: HealthOK})
		if err != nil {
			t.Fatal(err)
		}
	}
	lease, err := c.OpenReadLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	ctx := lease.Context(t.Context())
	match := func(record OrdinaryRecord) bool { return strings.Contains(strings.ToLower(record.Title), "über") }
	first, err := c.ListMatchingOrdinarySessions(ctx, OrdinaryPageRequest{Scope: "global", Limit: 2}, match)
	if err != nil || len(first) != 2 || first[0].TopicID != "135" || first[1].TopicID != "80" {
		t.Fatalf("sparse first page: %+v %v", first, err)
	}
	// A new match belongs only to a new database view.
	err = c.UpsertSession(t.Context(), SessionRecord{Path: filepath.Join(root, "079.jsonl"), Directory: root, Scope: "global",
		TopicID: "79", TopicTitle: "ÜBER new", LastActivityAt: 80, OrdinaryVisible: true, Health: HealthOK})
	if err != nil {
		t.Fatal(err)
	}
	last, err := c.ListMatchingOrdinarySessions(ctx, OrdinaryPageRequest{Scope: "global", Limit: 2, Cursor: first[1].Cursor}, match)
	if err != nil || len(last) != 1 || last[0].TopicID != "1" {
		t.Fatalf("sparse continuation lost its snapshot: %+v %v", last, err)
	}
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	calls := 0
	_, err = c.ListMatchingOrdinarySessions(readCtx, OrdinaryPageRequest{Scope: "global", Limit: 2}, func(OrdinaryRecord) bool {
		calls++
		cancel()
		return false
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("canceled metadata filter continued: calls=%d err=%v", calls, err)
	}
}
