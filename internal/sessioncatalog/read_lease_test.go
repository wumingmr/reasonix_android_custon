package sessioncatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"reasonix/internal/agent"
)

func TestReadLeaseAllowsWritesAndKeepsFlatPageSnapshot(t *testing.T) {
	c, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	r := SessionRecord{Scope: "global", Directory: t.TempDir(), TopicID: "same", TopicTitle: "Old", OrdinaryVisible: true, Health: HealthOK, TurnsState: TurnsUnknown, LastActivityAt: 1}
	r.Path = filepath.Join(r.Directory, "old.jsonl")
	if err := c.UpsertSession(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	lease, err := c.OpenReadLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	r.LastActivityAt = 2
	if err := c.UpsertSession(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	page, err := c.ListOrdinarySessions(lease.Context(t.Context()), OrdinaryPageRequest{Scope: "global", Limit: 1})
	if err != nil || len(page) != 1 || page[0].LastActivityAt != 1 {
		t.Fatalf("captured view: %+v %v", page, err)
	}
	page, err = c.ListOrdinarySessions(t.Context(), OrdinaryPageRequest{Scope: "global", Limit: 1})
	if err != nil || len(page) != 1 || page[0].LastActivityAt != 2 {
		t.Fatalf("current view: %+v %v", page, err)
	}
	lease.Close()
	lease.Close()
}

func TestOrdinaryGroupFiltersPhysicalSourcesAcrossCursorRanges(t *testing.T) {
	c, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	dir := t.TempDir()
	keys := []string{}
	for i := range 8 {
		path := filepath.Join(dir, fmt.Sprintf("%d.jsonl", i))
		topic := "shared"
		if i < 2 {
			topic = "pinned"
		}
		if err := c.UpsertSession(t.Context(), SessionRecord{Path: path, Directory: dir, Scope: "global", TopicID: topic,
			LastActivityAt: int64(i), CreatedAt: int64(i), OrdinaryVisible: true, Health: HealthOK}); err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			keys = append(keys, agent.SessionSourceKeyFromIdentity(PathIdentityKey(path), ""))
		}
	}
	if err := c.SyncMetadata(t.Context(), nil, []TopicMetadata{{Scope: "global", TopicID: "pinned", Pinned: true}}); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(keys)
	lease, err := c.OpenReadLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	for _, size := range []int{1, 2, 3} {
		for _, include := range []bool{true, false} {
			req := OrdinaryPageRequest{Scope: "global", Limit: size}
			want := []string{"0.jsonl", "6.jsonl", "4.jsonl", "2.jsonl"}
			if include {
				req.IncludeSourceKeysJSON = string(encoded)
			} else {
				req.ExcludeSourceKeysJSON = string(encoded)
				want = []string{"1.jsonl", "7.jsonl", "5.jsonl", "3.jsonl"}
			}
			got := []string{}
			for len(got) <= 8 {
				page, err := c.ListOrdinarySessions(lease.Context(t.Context()), req)
				if err != nil {
					t.Fatal(err)
				}
				if len(page) == 0 {
					break
				}
				for _, row := range page {
					got = append(got, filepath.Base(row.Path))
				}
				req.Cursor = page[len(page)-1].Cursor
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("size=%d include=%v got %v, want %v", size, include, got, want)
			}
		}
	}
	page, err := c.ListOrdinarySessions(lease.Context(t.Context()), OrdinaryPageRequest{Scope: "global", IncludeSourceKeysJSON: "[]"})
	if err != nil || len(page) != 0 {
		t.Fatalf("empty group admitted rows: %+v %v", page, err)
	}
}

func TestOrdinaryCursorCrossesPinnedActivityAndIdentityRangesExactlyOnce(t *testing.T) {
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	dir := t.TempDir()
	for i, topic := range []string{"p", "p", "p", "a", "a", "b", "c"} {
		activity := int64(30 - i/2)
		if i >= 3 {
			activity = 100
		}
		r := SessionRecord{Path: filepath.Join(dir, fmt.Sprintf("%d.jsonl", i)), Directory: dir, Scope: "global", TopicID: topic, CreatedAt: activity, LastActivityAt: activity, Health: HealthOK, TurnsState: TurnsUnknown, OrdinaryVisible: true}
		if err := c.UpsertSession(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.SyncMetadata(t.Context(), nil, []TopicMetadata{{Scope: "global", TopicID: "p", Title: "Pinned", Pinned: true}}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"activity", "created"} {
		for _, size := range []int{1, 2, 3} {
			req := OrdinaryPageRequest{Scope: "global", SortMode: mode, Limit: size}
			var paths []string
			for len(paths) <= 7 {
				page, err := c.ListOrdinarySessions(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				if len(page) == 0 {
					break
				}
				for _, row := range page {
					paths = append(paths, filepath.Base(row.Path))
				}
				req.Cursor = page[len(page)-1].Cursor
			}
			want := []string{"0.jsonl", "1.jsonl", "2.jsonl", "3.jsonl", "4.jsonl", "5.jsonl", "6.jsonl"}
			if !reflect.DeepEqual(paths, want) {
				t.Fatalf("%s/%d: %v", mode, size, paths)
			}
		}
	}
}

func TestOrdinaryPagePinIndexTracksMetadataAndSourceMoves(t *testing.T) {
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	dir := t.TempDir()
	a := SessionRecord{Path: filepath.Join(dir, "a.jsonl"), Directory: dir, Scope: "global", TopicID: "a", LastActivityAt: 20, Health: HealthOK, TurnsState: TurnsUnknown, OrdinaryVisible: true}
	b := a
	b.Path = filepath.Join(dir, "b.jsonl")
	b.TopicID = "b"
	b.LastActivityAt = 10
	for _, row := range []SessionRecord{a, b} {
		if err := c.UpsertSession(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}
	assertFirst := func(path string) {
		t.Helper()
		page, err := c.ListOrdinarySessions(t.Context(), OrdinaryPageRequest{Scope: "global", Limit: 1})
		if err != nil || len(page) != 1 || page[0].Path != path {
			t.Fatalf("first=%+v err=%v want %s", page, err, path)
		}
	}
	assertFirst(a.Path)
	if err := c.SyncMetadata(t.Context(), nil, []TopicMetadata{{Scope: "global", TopicID: "b", Title: "Pinned", Pinned: true}}); err != nil {
		t.Fatal(err)
	}
	assertFirst(b.Path)
	b.TopicID = "moved"
	if err := c.UpsertSession(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	assertFirst(a.Path)
	if err := c.SyncMetadata(t.Context(), nil, []TopicMetadata{{Scope: "global", TopicID: "new", Title: "New pinned", Pinned: true}}); err != nil {
		t.Fatal(err)
	}
	b.TopicID = "new"
	b.Path = filepath.Join(dir, "new.jsonl")
	if err := c.UpsertSession(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	assertFirst(b.Path)
}

func TestOrdinaryPageTimeFilterUsesFrozenInclusiveBoundary(t *testing.T) {
	c, err := Open(t.Context(), Options{InMemory: true, MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	dir := t.TempDir()
	for i, times := range [][2]int64{{99, 99}, {100, 90}, {80, 100}, {90, 120}} {
		r := SessionRecord{Path: filepath.Join(dir, fmt.Sprintf("%d.jsonl", i)), Directory: dir, Scope: "global", TopicID: "shared",
			CreatedAt: times[0], LastActivityAt: times[1], Health: HealthOK, TurnsState: TurnsUnknown, OrdinaryVisible: true}
		if err := c.UpsertSession(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"activity", "created"} {
		req := OrdinaryPageRequest{Scope: "global", SortMode: mode, Limit: 1, MinActivity: 100}
		seen := map[string]bool{}
		for {
			page, err := c.ListOrdinarySessions(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			name := filepath.Base(page[0].Path)
			if seen[name] || name == "0.jsonl" || len(seen) > 3 {
				t.Fatalf("%s repeated or admitted a pre-boundary record: %s", mode, name)
			}
			seen[name] = true
			req.Cursor = page[0].Cursor
		}
		if len(seen) != 3 {
			t.Fatalf("%s omitted inclusive created/activity boundary: %v", mode, seen)
		}
	}
}
