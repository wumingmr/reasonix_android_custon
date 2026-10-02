package sessioncatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestListTopicsFiltersGroupMembershipBeforePagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	catalog, err := Open(ctx, Options{InMemory: true, DisableRepair: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })

	for index, topicID := range []string{"a", "b", "c", "pinned"} {
		if err := catalog.UpsertSession(ctx, SessionRecord{
			Path: fmt.Sprintf("/sessions/%s.jsonl", topicID), Directory: "/sessions",
			Scope: "global", TopicID: topicID, TopicTitle: topicID,
			LastActivityAt: int64(100 - index), Turns: 1, TurnsState: TurnsValid, Health: HealthOK,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.SyncMetadata(ctx, nil, []TopicMetadata{{Scope: "global", TopicID: "pinned", Title: "pinned", Pinned: true}}); err != nil {
		t.Fatal(err)
	}

	include, _ := json.Marshal([]string{"b", "c", "pinned"})
	first, err := catalog.ListTopics(ctx, TopicPageRequest{
		Scope: "global", Limit: 1, IncludeTopicIDsJSON: string(include), ExcludePinned: true, CursorBinding: "group:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].TopicID != "b" || first.NextCursor == "" {
		t.Fatalf("first filtered page = %#v, want b with another page", first)
	}
	second, err := catalog.ListTopics(ctx, TopicPageRequest{
		Scope: "global", Limit: 1, Cursor: first.NextCursor, IncludeTopicIDsJSON: string(include), ExcludePinned: true, CursorBinding: "group:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].TopicID != "c" || second.NextCursor != "" {
		t.Fatalf("second filtered page = %#v, want c as final row", second)
	}
	if _, err := catalog.ListTopics(ctx, TopicPageRequest{
		Scope: "global", Limit: 1, Cursor: first.NextCursor, IncludeTopicIDsJSON: string(include), ExcludePinned: true, CursorBinding: "group:2",
	}); err == nil {
		t.Fatal("cursor from an older group membership revision must be rejected")
	}

	exclude, _ := json.Marshal([]string{"a", "b", "c"})
	unfilteredPinned, err := catalog.ListTopics(ctx, TopicPageRequest{Scope: "global", Limit: 10, ExcludeTopicIDsJSON: string(exclude)})
	if err != nil {
		t.Fatal(err)
	}
	if len(unfilteredPinned.Items) != 1 || unfilteredPinned.Items[0].TopicID != "pinned" {
		t.Fatalf("exclude membership page = %#v, want only pinned", unfilteredPinned)
	}
}

func TestListTopicsAcceptsLargeJSONGroupWithoutSQLiteParameterExpansion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	catalog, err := Open(ctx, Options{InMemory: true, DisableRepair: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close(context.Background()) })
	if err := catalog.UpsertSession(ctx, SessionRecord{
		Path: "/sessions/member.jsonl", Directory: "/sessions", Scope: "global",
		TopicID: "member", TopicTitle: "member", Turns: 1, TurnsState: TurnsValid, Health: HealthOK,
	}); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 1500)
	for index := range ids {
		ids[index] = fmt.Sprintf("topic-%04d", index)
	}
	ids[len(ids)-1] = "member"
	encoded, _ := json.Marshal(ids)
	page, err := catalog.ListTopics(ctx, TopicPageRequest{Scope: "global", Limit: 5, IncludeTopicIDsJSON: string(encoded)})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].TopicID != "member" {
		t.Fatalf("large group page = %#v, want member", page)
	}
}
