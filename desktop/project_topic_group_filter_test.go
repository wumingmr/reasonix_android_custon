package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/sessioncatalog"
)

func TestListProjectTopicsPaginatesCustomGroupsIndependently(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, "Grouped project"); err != nil {
		t.Fatal(err)
	}
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	installSessionCatalogForTest(t, app, dir, "project", root)
	catalog := app.sessionCatalog.Load()
	if catalog == nil {
		t.Fatal("session catalog not installed")
	}
	for index, topicID := range []string{"group-a", "group-b", "ungrouped", "pinned"} {
		if err := catalog.UpsertSession(context.Background(), sessioncatalog.SessionRecord{
			Path: filepath.Join(dir, topicID+".jsonl"), Directory: dir,
			Scope: "project", WorkspaceRoot: root, TopicID: topicID, TopicTitle: topicID,
			LastActivityAt: int64(100 - index), Turns: 1,
			TurnsState: sessioncatalog.TurnsValid, Health: sessioncatalog.HealthOK,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.SyncMetadata(context.Background(), nil, []sessioncatalog.TopicMetadata{{
		Scope: "project", WorkspaceRoot: root, TopicID: "pinned", Title: "pinned", Pinned: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveSessionGroups("project", root, []desktopGroup{
		{ID: "feature", Title: "Feature", TopicIDs: []string{"group-a", "group-b", "pinned"}},
		{ID: "empty", Title: "Empty", TopicIDs: []string{"missing"}},
	}); err != nil {
		t.Fatal(err)
	}

	first, err := app.ListProjectTopics(ProjectTopicPageRequest{
		Scope: "project", WorkspaceRoot: root, Limit: 1,
		GroupFilter: "group", GroupID: "feature", ExcludePinned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].TopicID != "group-a" || first.NextCursor == "" {
		t.Fatalf("first group page = %#v, want group-a and a next cursor", first)
	}
	second, err := app.ListProjectTopics(ProjectTopicPageRequest{
		Scope: "project", WorkspaceRoot: root, Limit: 1, Cursor: first.NextCursor,
		GroupFilter: "group", GroupID: "feature", ExcludePinned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].TopicID != "group-b" || second.NextCursor != "" {
		t.Fatalf("second group page = %#v, want group-b as final row", second)
	}

	ungrouped, err := app.ListProjectTopics(ProjectTopicPageRequest{
		Scope: "project", WorkspaceRoot: root, Limit: 10, GroupFilter: "ungrouped", ExcludePinned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ungrouped.Items) != 1 || ungrouped.Items[0].TopicID != "ungrouped" {
		t.Fatalf("ungrouped page = %#v, want only ungrouped", ungrouped)
	}
	empty, err := app.ListProjectTopics(ProjectTopicPageRequest{
		Scope: "project", WorkspaceRoot: root, Limit: 10, GroupFilter: "group", GroupID: "empty",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Fatalf("empty group page = %#v, want empty", empty)
	}
	if _, err := app.ListProjectTopics(ProjectTopicPageRequest{
		Scope: "project", WorkspaceRoot: root, GroupFilter: "group", GroupID: "deleted",
	}); err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("missing group error = %v, want explicit invalid-group error", err)
	}

	legacy, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy.Items) != 4 {
		t.Fatalf("legacy unfiltered page has %d rows, want 4", len(legacy.Items))
	}

	if err := app.SaveSessionGroups("project", root, []desktopGroup{{
		ID: "feature", Title: "Feature", TopicIDs: []string{"group-a"},
	}}); err != nil {
		t.Fatal(err)
	}
	if frozen, err := app.ListProjectTopics(ProjectTopicPageRequest{
		Scope: "project", WorkspaceRoot: root, Limit: 1, Cursor: first.NextCursor,
		GroupFilter: "group", GroupID: "feature", ExcludePinned: true,
	}); err != nil || frozen.SnapshotID != first.SnapshotID || len(frozen.Items) != 1 {
		t.Fatalf("group edit disturbed the frozen read: %+v %v", frozen, err)
	}
}

func TestResolveProjectTopicGroupFilterSupportsMaximumSizedGroup(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, "Large group"); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, maxSessionGroupTopics)
	for index := range ids {
		ids[index] = fmt.Sprintf("topic-%05d", index)
	}
	// This pure legacy-filter contract reads its legacy import representation.
	if err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		f.Projects[projectIndexByRoot(f.Projects, root)].Groups = []desktopGroup{{ID: "large", Title: "Large", TopicIDs: ids}}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	req := ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, GroupFilter: "group", GroupID: "large"}
	if err := resolveProjectTopicGroupFilter(&req); err != nil {
		t.Fatal(err)
	}
	if len(req.groupInclude) != maxSessionGroupTopics || !strings.HasPrefix(req.groupIncludeJSON, "[") {
		t.Fatalf("large group filter contains %d members and JSON %q", len(req.groupInclude), req.groupIncludeJSON[:min(20, len(req.groupIncludeJSON))])
	}
}

func TestProjectTopicGroupCursorBindingCoversListIdentity(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	otherRoot := t.TempDir()
	if err := addProject(root, "Bound project"); err != nil {
		t.Fatal(err)
	}
	if err := addProject(otherRoot, "Other bound project"); err != nil {
		t.Fatal(err)
	}
	for _, projectRoot := range []string{root, otherRoot} {
		if err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
			f.Projects[projectIndexByRoot(f.Projects, projectRoot)].Groups = []desktopGroup{{ID: "feature", Title: "Feature", TopicIDs: []string{"a", "b"}}}
			return true, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	base := ProjectTopicPageRequest{
		Scope: "project", WorkspaceRoot: root, Query: "needle", SortMode: "updated",
		GroupFilter: "group", GroupID: "feature", ExcludePinned: true,
	}
	resolve := func(req ProjectTopicPageRequest) string {
		t.Helper()
		if err := resolveProjectTopicGroupFilter(&req); err != nil {
			t.Fatal(err)
		}
		return req.groupCursorBind
	}
	binding := resolve(base)
	if binding == "" {
		t.Fatal("new group request must produce a bound cursor")
	}
	variants := []ProjectTopicPageRequest{base, base, base}
	variants[0].Query = "other"
	variants[1].SortMode = "created"
	variants[2].WorkspaceRoot = otherRoot
	for _, variant := range variants {
		if got := resolve(variant); got == binding {
			t.Fatalf("cursor binding %q did not change for %#v", got, variant)
		}
	}
	legacy := ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root}
	if got := resolve(legacy); got != "" {
		t.Fatalf("legacy request binding = %q, want empty", got)
	}
}

func TestMetadataFallbackBindsGroupCursorToMembershipRevision(t *testing.T) {
	app, root, _ := canonicalOrganizationFixture(t, "a", "b", "c")
	if err := app.SaveSessionGroups("project", root, []desktopGroup{{
		ID: "feature", Title: "Feature", TopicIDs: []string{"a", "b"},
	}}); err != nil {
		t.Fatal(err)
	}
	first, err := app.ListProjectTopics(ProjectTopicPageRequest{
		Scope: "project", WorkspaceRoot: root, Limit: 1, GroupFilter: "group", GroupID: "feature",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("metadata first page = %#v, want one row and bound cursor", first)
	}
	if err := app.SaveSessionGroups("project", root, []desktopGroup{{
		ID: "feature", Title: "Feature", TopicIDs: []string{"a"},
	}}); err != nil {
		t.Fatal(err)
	}
	if frozen, err := app.ListProjectTopics(ProjectTopicPageRequest{
		Scope: "project", WorkspaceRoot: root, Limit: 1, Cursor: first.NextCursor,
		GroupFilter: "group", GroupID: "feature",
	}); err != nil || frozen.SnapshotID != first.SnapshotID || len(frozen.Items) != 1 {
		t.Fatalf("metadata group edit disturbed the frozen read: %+v %v", frozen, err)
	}
}

func TestGetTopicSummaryHonorsCatalogFallbackCompleteness(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, "Summary project"); err != nil {
		t.Fatal(err)
	}
	if err := setTopicTitle(root, "metadata-only", "Metadata only"); err != nil {
		t.Fatal(err)
	}
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("unscanned catalog keeps metadata continuity", func(t *testing.T) {
		app := NewApp()
		catalog, err := sessioncatalog.Open(context.Background(), sessioncatalog.Options{InMemory: true, DisableRepair: true})
		if err != nil {
			t.Fatal(err)
		}
		app.sessionCatalog.Store(catalog)
		t.Cleanup(func() { app.stopSessionCatalog(time.Second) })
		summary, err := app.GetTopicSummary(ProjectTopicKey{Scope: "project", WorkspaceRoot: root, TopicID: "metadata-only"})
		if err != nil || summary.TopicID != "metadata-only" {
			t.Fatalf("incomplete summary = %#v, err=%v, want metadata continuity", summary, err)
		}
	})

	t.Run("complete catalog does not resurrect absent metadata", func(t *testing.T) {
		app := NewApp()
		installSessionCatalogForTest(t, app, dir, "project", root)
		summary, err := app.GetTopicSummary(ProjectTopicKey{Scope: "project", WorkspaceRoot: root, TopicID: "metadata-only"})
		if err != nil {
			t.Fatal(err)
		}
		if summary.TopicID != "" || summary.Children == nil {
			t.Fatalf("complete summary = %#v, want an empty non-null result", summary)
		}
	})
}

func TestSessionGroupCanSplitRowsSharingTopicID(t *testing.T) {
	app, root, refs := canonicalOrganizationFixtureWithTopic(t, "shared", "a", "b")
	bRef := refs["b"]
	b := ProjectNode{Key: "b", Kind: "topic", TopicID: "shared", Session: &bRef}
	group := desktopGroup{
		ID: "one", Title: "One", TopicIDs: []string{"shared"},
		ExcludedSessionKeys: []string{projectNodeSessionKey(b)},
	}
	if err := app.SaveSessionGroups("project", root, []desktopGroup{group}); err != nil {
		t.Fatal(err)
	}
	grouped, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, GroupFilter: "group", GroupID: "one", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(grouped.Items) != 1 || grouped.Items[0].Session == nil || grouped.Items[0].Session.SessionID != "a" {
		t.Fatalf("group membership did not isolate A: %#v", grouped.Items)
	}
	ungrouped, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, GroupFilter: "ungrouped", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(ungrouped.Items) != 1 || ungrouped.Items[0].Session == nil || ungrouped.Items[0].Session.SessionID != "b" {
		t.Fatalf("ungrouped membership did not isolate B: %#v", ungrouped.Items)
	}
}
