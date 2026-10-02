package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
	"reasonix/internal/sessioncatalog"
)

func TestMetadataTopicTextFilterUsesLazyUnicodeSnapshot(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, "Text filter"); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	app.sessionCatalog.Store(catalog)
	t.Cleanup(func() { app.desktopSessions.readSnapshots.close(); app.stopSessionCatalog(time.Second) })
	rows := make([]sessioncatalog.SessionRecord, 123)
	for i := range rows {
		title, preview := "unrelated", ""
		if i == 121 || i == 3 {
			title = "ÜBER 历史"
		}
		if i == 60 {
			preview = "über preview"
		}
		rows[i] = sessioncatalog.SessionRecord{Path: filepath.Join(root, fmt.Sprintf("unread-%03d.jsonl", i)), Directory: root,
			Scope: "project", WorkspaceRoot: root, TopicID: fmt.Sprint(i), TopicTitle: title, Preview: preview,
			CreatedAt: int64(i + 1), LastActivityAt: int64(i + 1), OrdinaryVisible: true, Health: sessioncatalog.HealthOK}
		if err := catalog.UpsertSession(t.Context(), rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	req := ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Query: " üBeR ", Limit: 1}
	first, err := app.ListProjectTopics(req)
	if err != nil || len(first.Items) != 1 || first.Items[0].SessionPath != rows[121].Path || first.NextCursor == "" {
		t.Fatalf("Unicode first page: %+v %v", first, err)
	}
	store := &app.desktopSessions.readSnapshots
	store.mu.Lock()
	snapshot := store.entries[first.SnapshotID].data
	store.mu.Unlock()
	if snapshot.readPage == nil || snapshot.count != 0 || len(snapshot.rows) != 0 || snapshot.db != nil {
		t.Fatal("text filter materialized the history library")
	}
	rows[120].TopicTitle = "ÜBER newly matching"
	if err := catalog.UpsertSession(t.Context(), rows[120]); err != nil {
		t.Fatal(err)
	}
	page := first
	for _, index := range []int{60, 3} {
		req.Cursor = page.NextCursor
		page, err = app.ListProjectTopics(req)
		if err != nil || len(page.Items) != 1 || page.Items[0].SessionPath != rows[index].Path {
			t.Fatalf("fixed text snapshot continuation: %+v %v", page, err)
		}
	}
	if page.NextCursor != "" {
		t.Fatal("filtered tail advertised more matches")
	}
	app.ReleaseReadSnapshot(first.SnapshotID)
	req.Cursor, req.Query = "", strings.ToUpper(desktopSourceKey(rows[50].Path, ""))
	identity, err := app.ListProjectTopics(req)
	if err != nil || len(identity.Items) != 1 || identity.Items[0].SessionPath != rows[50].Path {
		t.Fatalf("physical source key search changed: %+v %v", identity, err)
	}
	app.ReleaseReadSnapshot(identity.SnapshotID)
	req.Query = "missing term"
	empty, err := app.ListProjectTopics(req)
	if err != nil || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Fatalf("empty text result: %+v %v", empty, err)
	}
	app.ReleaseReadSnapshot(empty.SnapshotID)
	app.setDesktopLocale("en")
	if err := catalog.SyncMetadata(t.Context(), nil, []sessioncatalog.TopicMetadata{{Scope: "project", WorkspaceRoot: root,
		TopicID: rows[0].TopicID, Title: defaultTopicTitle, TitleSource: topicTitleSourceAuto}}); err != nil {
		t.Fatal(err)
	}
	req.Query = defaultTopicTitleEn
	localized, err := app.ListProjectTopics(req)
	if err != nil || len(localized.Items) != 1 || localized.Items[0].SessionPath != rows[0].Path || localized.Items[0].Label != defaultTopicTitleEn {
		t.Fatalf("localized auto-title filter changed: %+v %v", localized, err)
	}
	app.ReleaseReadSnapshot(localized.SnapshotID)
}

func TestMetadataTopicSnapshotReadsPagesWithoutMaterializingHistory(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, "Large history"); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	app.sessionCatalog.Store(catalog)
	t.Cleanup(func() { app.desktopSessions.readSnapshots.close(); app.stopSessionCatalog(time.Second) })
	rows := make([]sessioncatalog.SessionRecord, 203)
	for i := range rows {
		// No source files exist: this list can only succeed by reading metadata.
		rows[i] = sessioncatalog.SessionRecord{Path: filepath.Join(root, fmt.Sprintf("old-%04d.jsonl", i)), Directory: root, Scope: "project", WorkspaceRoot: root, TopicID: "shared-topic", TopicTitle: "Shared", CreatedAt: int64(i + 1), LastActivityAt: int64(i + 1), OrdinaryVisible: true, TurnsState: sessioncatalog.TurnsUnknown, Health: sessioncatalog.HealthOK}
		if err := catalog.UpsertSession(t.Context(), rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	req := ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 200}
	first, err := app.ListProjectTopics(req)
	if err != nil || len(first.Items) != 200 || first.NextCursor == "" {
		t.Fatalf("first page: %d %q %v", len(first.Items), first.NextCursor, err)
	}
	store := &app.desktopSessions.readSnapshots
	store.mu.Lock()
	snapshot := store.entries[first.SnapshotID].data
	store.mu.Unlock()
	if snapshot.readPage == nil || snapshot.count != 0 || len(snapshot.rows) != 0 || snapshot.db != nil {
		t.Fatal("first page materialized history instead of retaining a catalog view")
	}
	// Concurrent activity changes ordering for a new view only.
	rows[0].LastActivityAt = 10000
	if err := catalog.UpsertSession(context.Background(), rows[0]); err != nil {
		t.Fatal(err)
	}
	req.Cursor = first.NextCursor
	second, err := app.ListProjectTopics(req)
	if err != nil || len(second.Items) != 3 || second.NextCursor != "" || second.Items[2].SessionPath != rows[0].Path {
		t.Fatalf("snapshot continuation: %+v %v", second, err)
	}
	req.Cursor = ""
	fresh, err := app.ListProjectTopics(req)
	if err != nil || fresh.Items[0].SessionPath != rows[0].Path {
		t.Fatalf("refresh: %+v %v", fresh, err)
	}
	app.ReleaseReadSnapshot(first.SnapshotID)
	if !snapshot.released || snapshot.closeRead != nil {
		t.Fatal("released cursor retained the WAL view")
	}
}

func TestMetadataTopicGroupSnapshotKeepsMembershipAndPagesLazy(t *testing.T) {
	app, root, _ := canonicalOrganizationFixture(t, "inside", "outside")
	catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	app.sessionCatalog.Store(catalog)
	t.Cleanup(func() { app.desktopSessions.readSnapshots.close(); app.stopSessionCatalog(time.Second) })
	workspaceID, _, err := app.ensureSessionOrganization("project", root)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for i := range 9 {
		path := filepath.Join(root, fmt.Sprintf("unread-%d.jsonl", i))
		paths = append(paths, path)
		if err := catalog.UpsertSession(t.Context(), sessioncatalog.SessionRecord{Path: path, Directory: root,
			Scope: "project", WorkspaceRoot: root, TopicID: "shared", TopicTitle: "Shared",
			CreatedAt: int64(i + 1), LastActivityAt: int64(i + 1), OrdinaryVisible: true, Health: sessioncatalog.HealthOK}); err != nil {
			t.Fatal(err)
		}
	}
	sourceKey := func(i int) string { return "source\x00local\x00" + desktopSourceKey(paths[i], "") }
	_, _, err = app.workspaceRegistry().UpdateOrganization(t.Context(), workspaceID, nil, func(org *workspacestate.Organization) error {
		org.Groups = []workspacestate.OrganizationGroup{
			{ID: "selected", Members: []string{workspacestate.SessionKey("inside"), sourceKey(1), sourceKey(8)}},
			{ID: "empty", Members: []string{}},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := &recordingTopicInfoReader{}
	req := ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, GroupFilter: "group", GroupID: "selected", Limit: 1}
	page, err := app.readProjectTopicPage(req, reader)
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("first grouped page: %+v %v", page, err)
	}
	if !reflect.DeepEqual(reader.ids, []string{"inside"}) {
		t.Fatalf("group read unrelated canonical headers: %v", reader.ids)
	}
	store := &app.desktopSessions.readSnapshots
	store.mu.Lock()
	snapshot := store.entries[page.SnapshotID].data
	store.mu.Unlock()
	if snapshot.readPage == nil || snapshot.count != 0 || len(snapshot.rows) != 0 || snapshot.db != nil {
		t.Fatal("group materialized the history library")
	}
	// A later membership change belongs to a fresh snapshot. The retained
	// cursor must keep all three original members and their fixed order.
	_, _, err = app.workspaceRegistry().UpdateOrganization(t.Context(), workspaceID, nil, func(org *workspacestate.Organization) error {
		org.Groups[0].Members = []string{workspacestate.SessionKey("outside"), sourceKey(0)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := []string{projectNodeSessionKey(page.Items[0])}
	for page.NextCursor != "" && len(seen) <= 9 {
		req.Cursor = page.NextCursor
		page, err = app.readProjectTopicPage(req, reader)
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range page.Items {
			seen = append(seen, projectNodeSessionKey(node))
		}
	}
	want := []string{sourceKey(8), sourceKey(1), workspacestate.SessionKey("inside")}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("retained membership: %v, want %v", seen, want)
	}
	app.ReleaseReadSnapshot(page.SnapshotID)
	reader.ids = nil
	req.Cursor, req.Limit = "", 50
	fresh, err := app.readProjectTopicPage(req, reader)
	if err != nil || len(fresh.Items) != 2 || !reflect.DeepEqual(reader.ids, []string{"outside"}) {
		t.Fatalf("fresh group: %+v headers=%v %v", fresh, reader.ids, err)
	}
	app.ReleaseReadSnapshot(fresh.SnapshotID)
	reader.ids = nil
	req.GroupID = "empty"
	empty, err := app.readProjectTopicPage(req, reader)
	if err != nil || len(empty.Items) != 0 || len(reader.ids) != 0 {
		t.Fatalf("empty group: %+v headers=%v %v", empty, reader.ids, err)
	}
	app.ReleaseReadSnapshot(empty.SnapshotID)
	req.GroupFilter, req.GroupID = "ungrouped", ""
	ungrouped, err := app.readProjectTopicPage(req, reader)
	if err != nil || len(ungrouped.Items) != 9 || !reflect.DeepEqual(reader.ids, []string{"inside"}) {
		t.Fatalf("ungrouped: %+v headers=%v %v", ungrouped, reader.ids, err)
	}
	for _, node := range ungrouped.Items {
		if key := projectNodeSessionKey(node); key == sourceKey(0) || key == workspacestate.SessionKey("outside") {
			t.Fatalf("grouped member appeared ungrouped: %s", key)
		}
	}
	app.ReleaseReadSnapshot(ungrouped.SnapshotID)
	req.GroupFilter, req.GroupID = "group", "deleted"
	if _, err := app.readProjectTopicPage(req, reader); err == nil {
		t.Fatal("deleted group silently returned all history")
	}
}

func TestMetadataTopicSnapshotAllViewWithGroupsStaysLazy(t *testing.T) {
	app, root, _ := canonicalOrganizationFixture(t, "pinned", "not-pinned")
	catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	app.sessionCatalog.Store(catalog)
	t.Cleanup(func() { app.desktopSessions.readSnapshots.close(); app.stopSessionCatalog(time.Second) })
	workspaceID, _, err := app.ensureSessionOrganization("project", root)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = app.workspaceRegistry().UpdateOrganization(t.Context(), workspaceID, nil, func(org *workspacestate.Organization) error {
		org.Groups = []workspacestate.OrganizationGroup{{ID: "g", Title: "Group", Members: []string{workspacestate.SessionKey("pinned")}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pin := true
	if err := app.workspaceRegistry().UpdatePresentation(t.Context(), []string{"pinned"}, nil, &pin); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if err := catalog.UpsertSession(t.Context(), sessioncatalog.SessionRecord{
			Path: filepath.Join(root, fmt.Sprintf("unread-%d.jsonl", i)), Directory: root,
			Scope: "project", WorkspaceRoot: root, TopicID: fmt.Sprintf("topic-%d", i), TopicTitle: "Legacy",
			CreatedAt: 1, LastActivityAt: 1, OrdinaryVisible: true, Health: sessioncatalog.HealthOK,
		}); err != nil {
			t.Fatal(err)
		}
	}
	req := ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, GroupFilter: "all", Limit: 2}
	page, err := app.ListProjectTopics(req)
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("all view: %+v %v", page, err)
	}
	store := &app.desktopSessions.readSnapshots
	store.mu.Lock()
	lazy := store.entries[page.SnapshotID].data.readPage != nil
	store.mu.Unlock()
	if !lazy {
		t.Fatal("an unrelated group forced the all view to materialize legacy history")
	}
	app.ReleaseReadSnapshot(page.SnapshotID)
	timed := req
	timed.TimeFilter, timed.Limit = "24h", 1
	recent, err := app.ListProjectTopics(timed)
	if err != nil || len(recent.Items) != 1 || recent.NextCursor == "" {
		t.Fatalf("time-filtered first page: %+v %v", recent, err)
	}
	store.mu.Lock()
	lazy = store.entries[recent.SnapshotID].data.readPage != nil
	store.mu.Unlock()
	if !lazy {
		t.Fatal("time filter materialized the history library")
	}
	timed.Cursor = recent.NextCursor
	last, err := app.ListProjectTopics(timed)
	if err != nil || len(last.Items) != 1 || last.NextCursor != "" || last.Items[0].Session == nil {
		t.Fatalf("time filter admitted old legacy metadata: %+v %v", last, err)
	}
	app.ReleaseReadSnapshot(recent.SnapshotID)
	state, err := app.workspaceRegistry().LoadProjection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Shell construction uses the same lazy adapter, including an unmigrated
	// automatic-order preference. It must not enumerate unpinned histories.
	pins, err := app.projectTopicsFromProjection(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 2, pinnedOnly: true}, state, workspacestate.NewWorkspaceIndex(state), workspaceID, workspacestate.Organization{}, &desktopProject{})
	if err != nil || len(pins.Items) != 1 || pins.Items[0].Session.SessionID != "pinned" {
		t.Fatalf("pinned shell: %+v %v", pins, err)
	}
	store.mu.Lock()
	lazy = store.entries[pins.SnapshotID].data.readPage != nil
	store.mu.Unlock()
	if !lazy {
		t.Fatal("pinned shell bypassed the lazy catalog adapter")
	}
	app.ReleaseReadSnapshot(pins.SnapshotID)
	// Independently assert which canonical headers are admitted to this read.
	reader := &recordingTopicInfoReader{}
	admitted, err := app.readProjectTopicPage(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, pinnedOnly: true}, reader)
	if err != nil || len(reader.ids) != 1 || reader.ids[0] != "pinned" {
		t.Fatalf("pinned metadata admission: %v err=%v", reader.ids, err)
	}
	app.ReleaseReadSnapshot(admitted.SnapshotID)
}

type recordingTopicInfoReader struct {
	ids    []string
	onStat func()
}

func (r *recordingTopicInfoReader) Stat(_ context.Context, ref session.SessionRef) (session.SessionInfo, error) {
	r.ids = append(r.ids, ref.SessionID)
	if r.onStat != nil {
		r.onStat()
	}
	return session.SessionInfo{SessionID: ref.SessionID}, nil
}

func TestMetadataTopicSnapshotFencesTopicChangeAfterWALCapture(t *testing.T) {
	app, root, _ := canonicalOrganizationFixture(t, "current")
	catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	app.sessionCatalog.Store(catalog)
	t.Cleanup(func() { app.desktopSessions.readSnapshots.close(); app.stopSessionCatalog(time.Second) })
	record := sessioncatalog.SessionRecord{Path: filepath.Join(root, "old.jsonl"), Directory: root, Scope: "project", WorkspaceRoot: root, TopicID: "before", TopicTitle: "Before", CreatedAt: 1, LastActivityAt: 1, Health: sessioncatalog.HealthOK, OrdinaryVisible: true}
	if err := catalog.UpsertSession(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SyncMetadata(t.Context(), nil, []sessioncatalog.TopicMetadata{{Scope: "project", WorkspaceRoot: root, TopicID: "before", Title: "Before", Pinned: true}}); err != nil {
		t.Fatal(err)
	}
	reader := &recordingTopicInfoReader{onStat: func() {
		// Header reads happen after the catalog read transaction is captured.
		// Move the physical source while that older view is still retained.
		record.TopicID, record.TopicTitle = "after", "After"
		if err := catalog.UpsertSession(t.Context(), record); err != nil {
			t.Fatal(err)
		}
	}}
	req := ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 1}
	first, err := app.readProjectTopicPage(req, reader)
	if err != nil || len(first.Items) != 1 || first.Items[0].TopicID != "before" || first.NextCursor == "" {
		t.Fatalf("captured first page: %+v %v", first, err)
	}
	defer app.ReleaseReadSnapshot(first.SnapshotID)
	req.Cursor = first.NextCursor
	_, err = app.readProjectTopicPage(req, reader)
	var stale *SessionOperationError
	if !errors.As(err, &stale) || stale.Code != "stale_cursor" {
		t.Fatalf("live identity replaced the frozen source fence: %v", err)
	}
}
