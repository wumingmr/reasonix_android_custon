package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

func TestCompleteTopicOrderPreservesTopicsMissingFromPartialClient(t *testing.T) {
	got, err := completeTopicOrder([]string{"c", "a"}, []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"c", "b", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if _, err := completeTopicOrder([]string{"a", "a"}, []string{"a", "b"}); err == nil {
		t.Fatal("duplicate topic order must fail")
	}
	if _, err := completeTopicOrder([]string{"unknown"}, []string{"a", "b"}); err == nil {
		t.Fatal("unknown topic must not be persisted")
	}
}

func TestReorderTopicsEnablesManualOrderOnlyAfterExplicitDrag(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, "Project"); err != nil {
		t.Fatal(err)
	}
	if err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		i := projectIndexByRoot(f.Projects, root)
		f.Projects[i].Topics = []string{"a", "b", "c"}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	for _, topic := range app.metadataProjectTopics("project", root) {
		if topic.SortOrder != -1 {
			t.Fatalf("preference-free sortOrder = %d, want -1", topic.SortOrder)
		}
	}
	if err := app.ReorderTopics("project", root, []string{"c", "a"}); err != nil {
		t.Fatal(err)
	}
	project := loadProjectsFile().Projects[projectIndexByRoot(loadProjectsFile().Projects, root)]
	if want := []string{"c", "b", "a"}; !reflect.DeepEqual(project.Topics, want) {
		t.Fatalf("topics = %v, want %v", project.Topics, want)
	}
	if !project.ManualTopicOrder {
		t.Fatal("manual topic order flag was not persisted")
	}
	for index, topic := range app.metadataProjectTopics("project", root) {
		if topic.SortOrder != index {
			t.Fatalf("topic %q sortOrder = %d, want %d", topic.TopicID, topic.SortOrder, index)
		}
	}
}

func TestSessionGroupsPersistExclusiveMembership(t *testing.T) {
	app, root, refs := canonicalOrganizationFixture(t, "a", "b", "c")
	groups := []desktopGroup{
		{ID: "one", Title: "One", TopicIDs: []string{"a", "b"}},
		{ID: "two", Title: "Two", TopicIDs: []string{"c"}},
	}
	if err := app.SaveSessionGroups("project", root, groups); err != nil {
		t.Fatal(err)
	}
	got, err := app.ListProjectGroups("project", root)
	if err != nil {
		t.Fatal(err)
	}
	want := []desktopGroup{
		{ID: "one", Title: "One", SessionKeys: []string{workspacestate.SessionKey("a"), workspacestate.SessionKey("b")}},
		{ID: "two", Title: "Two", SessionKeys: []string{workspacestate.SessionKey("c")}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %#v, want explicit members %#v", got, want)
	}
	if err := app.ArchiveCanonicalSession(refs["b"]); err != nil {
		t.Fatal(err)
	}
	page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, GroupFilter: "group", GroupID: "one", Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Session == nil || page.Items[0].Session.SessionID != "a" {
		t.Fatalf("group after B archive = %#v, err=%v", page, err)
	}
	if err := app.SaveSessionGroups("project", root, []desktopGroup{
		{ID: "one", Title: "One", SessionKeys: []string{workspacestate.SessionKey("a")}},
		{ID: "two", Title: "Two", SessionKeys: []string{workspacestate.SessionKey("a")}},
	}); err == nil {
		t.Fatal("a session cannot belong to multiple groups")
	}
	if err := app.SaveSessionGroups("project", root, []desktopGroup{
		{ID: "one", Title: "One", TopicIDs: []string{"a"}},
		{ID: "two", Title: "Two", TopicIDs: []string{"a"}},
	}); err == nil {
		t.Fatal("legacy duplicate topic membership must still fail")
	}
	if _, err := app.ListProjectGroups("other", root); err == nil {
		t.Fatal("unsupported scope must fail")
	}
}

func TestProjectOrganizationSurvivesOlderBuildRoundTrip(t *testing.T) {
	app, root, _ := canonicalOrganizationFixture(t, "a", "b", "c")
	legacyGroups := []desktopGroup{{ID: "important", Title: "Important", TopicIDs: []string{"a", "c"}}}
	if err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		p := &f.Projects[projectIndexByRoot(f.Projects, root)]
		p.Topics = []string{"c", "a", "b"}
		p.ManualTopicOrder = true
		p.SessionOrder = []string{workspacestate.SessionKey("c"), workspacestate.SessionKey("a"), workspacestate.SessionKey("b")}
		p.ManualSessionOrder = true
		p.Groups = legacyGroups
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	workspace := SessionOrganizationWorkspace{Scope: "project", WorkspaceRoot: root}
	imported, err := app.GetSessionOrganization(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Groups) != 1 || !reflect.DeepEqual(imported.Groups[0].SessionKeys, []string{workspacestate.SessionKey("a"), workspacestate.SessionKey("c")}) {
		t.Fatalf("legacy grouping was not imported: %#v", imported)
	}
	if _, err := os.Stat(filepath.Join(desktopConfigDir(), desktopProjectOrganizationFile)); err != nil {
		t.Fatalf("legacy import sidecar missing: %v", err)
	}
	if err := app.ReorderSessions("project", root, []string{workspacestate.SessionKey("b"), workspacestate.SessionKey("c"), workspacestate.SessionKey("a")}); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveSessionGroups("project", root, []desktopGroup{{ID: "important", Title: "Independent", SessionKeys: []string{workspacestate.SessionKey("c")}}}); err != nil {
		t.Fatal(err)
	}
	before, err := app.GetSessionOrganization(workspace)
	if err != nil {
		t.Fatal(err)
	}
	// An older project's typed round trip drops its unknown organization fields.
	// The migrated registry must retain the user's newer independent choices.
	projectsPath := filepath.Join(desktopConfigDir(), desktopProjectsFile)
	b, err := os.ReadFile(projectsPath)
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]any
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatal(err)
	}
	delete(old, "globalManualTopicOrder")
	delete(old, "globalGroups")
	for _, value := range old["projects"].([]any) {
		p := value.(map[string]any)
		for _, field := range []string{"manualTopicOrder", "groups", "sessionOrder", "manualSessionOrder"} {
			delete(p, field)
		}
	}
	b, err = json.MarshalIndent(old, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectsPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	after, err := app.GetSessionOrganization(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("legacy project write overwrote registry organization: before=%#v after=%#v", before, after)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 3 {
		t.Fatalf("registry version=%d, want downgrade-protected v3", state.Version)
	}
}

func TestVersionedSessionGroupsRejectStaleFullState(t *testing.T) {
	app, root, refs := canonicalOrganizationFixture(t, "a")
	key := workspacestate.SessionKey("a")
	if err := app.SaveSessionGroups("project", root, []desktopGroup{{ID: "one", Title: "One", SessionKeys: []string{key}}}); err != nil {
		t.Fatal(err)
	}
	base, err := app.GetProjectGroups("project", root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := app.SaveSessionGroupsVersioned("project", root, base.Revision, []desktopGroup{{ID: "one", Title: "Renamed", SessionKeys: []string{key}}})
	if err != nil || !first.Applied {
		t.Fatalf("first CAS=%#v err=%v", first, err)
	}
	// A second app window shares the registry but retains the old revision.
	second := NewApp()
	t.Cleanup(second.closeSessionServices)
	second.ctx = t.Context()
	second.desktopSessions.root = app.desktopSessions.root
	second.desktopSessions.workspaceState = workspacestate.NewStore(app.workspaceRegistry().Path())
	stale, err := second.SaveSessionGroupsVersioned("project", root, base.Revision, []desktopGroup{{ID: "one", Title: "One", SessionKeys: []string{key}}, {ID: "two", Title: "Two"}})
	if err != nil {
		t.Fatal(err)
	}
	if stale.Applied || stale.Revision != first.Revision || len(stale.Groups) != 1 || stale.Groups[0].Title != "Renamed" {
		t.Fatalf("stale CAS=%#v", stale)
	}
	rebased := append(nonNilGroups(stale.Groups), desktopGroup{ID: "two", Title: "Two"})
	merged, err := second.SaveSessionGroupsVersioned("project", root, stale.Revision, rebased)
	if err != nil || !merged.Applied || len(merged.Groups) != 2 || merged.Groups[0].Title != "Renamed" {
		t.Fatalf("rebased CAS=%#v err=%v", merged, err)
	}
	beforeArchive, err := app.GetProjectGroups("project", root)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.ArchiveCanonicalSession(refs["a"]); err != nil {
		t.Fatal(err)
	}
	// Explicitly removing the archived member is another window's newer choice.
	removed, err := second.SaveSessionGroupsVersioned("project", root, beforeArchive.Revision, []desktopGroup{{ID: "one", Title: "Renamed"}, {ID: "two", Title: "Two"}})
	if err != nil || !removed.Applied {
		t.Fatalf("remove archived membership=%#v err=%v", removed, err)
	}
	resurrect, err := app.SaveSessionGroupsVersioned("project", root, beforeArchive.Revision, beforeArchive.Groups)
	if err != nil {
		t.Fatal(err)
	}
	if resurrect.Applied || len(resurrect.Groups[0].SessionKeys) != 0 {
		t.Fatalf("stale save overwrote archived membership removal: %#v", resurrect)
	}
	// A legacy topic payload cannot bring an archived canonical member back.
	if err := app.SaveSessionGroups("project", root, []desktopGroup{{ID: "one", Title: "Renamed", TopicIDs: []string{"a"}}, {ID: "two", Title: "Two"}}); err != nil {
		t.Fatal(err)
	}
	legacy, err := app.GetProjectGroups("project", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy.Groups[0].SessionKeys) != 0 {
		t.Fatalf("legacy save resurrected archived membership: %#v", legacy)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil || state.SessionStates["a"].Lifecycle != workspacestate.Archived {
		t.Fatalf("archive lifecycle lost after group writes: %#v err=%v", state.SessionStates["a"], err)
	}
	page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("archived session reappeared: %#v err=%v", page, err)
	}
}

func TestListProjectGroupsMissingProjectReturnsJSONArray(t *testing.T) {
	isolateDesktopUserDirs(t)
	groups, err := NewApp().ListProjectGroups("project", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(groups)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "[]" {
		t.Fatalf("missing project groups JSON = %s, want []", b)
	}
}

func TestReorderSessionsPersistsIndependentSharedTopicOrder(t *testing.T) {
	app, root, _ := canonicalOrganizationFixtureWithTopic(t, "shared", "a", "b")
	keys := []string{workspacestate.SessionKey("b"), workspacestate.SessionKey("a")}
	if err := app.ReorderSessions("project", root, keys); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	organization := state.Workspaces[desktopWorkspaceID("project", root)].Organization
	if organization == nil || !organization.ManualOrderEnabled || !reflect.DeepEqual(organization.Order, keys) {
		t.Fatalf("registry order=%#v", organization)
	}
	page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Session.SessionID != "b" || page.Items[1].Session.SessionID != "a" {
		t.Fatalf("sidebar order=%#v", page.Items)
	}
	if err := app.ReorderSessions("project", root, []string{"path\x00/does-not-exist.jsonl"}); err == nil {
		t.Fatal("unknown source identity must be rejected")
	}
}

func canonicalOrganizationFixture(t *testing.T, ids ...string) (*App, string, map[string]session.SessionRef) {
	t.Helper()
	return canonicalOrganizationFixtureWithTopic(t, "", ids...)
}

func canonicalOrganizationFixtureWithTopic(t *testing.T, sharedTopic string, ids ...string) (*App, string, map[string]session.SessionRef) {
	t.Helper()
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, "Organization test"); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.ctx = t.Context()
	app.desktopSessions.root = filepath.Join(root, "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "workspace-state.json"))
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]session.SessionRef{}
	for _, id := range ids {
		runtime, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: id, CWD: root, Origin: session.SessionOriginNew})
		if err != nil {
			t.Fatal(err)
		}
		if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspaceID, id, ""); err != nil {
			t.Fatal(err)
		}
		topic := id
		if sharedTopic != "" {
			topic = sharedTopic
		}
		if err := app.workspaceRegistry().EnsureSessionTopic(t.Context(), id, topic, id); err != nil {
			t.Fatal(err)
		}
		refs[id] = runtime.Ref()
	}
	return app, root, refs
}
