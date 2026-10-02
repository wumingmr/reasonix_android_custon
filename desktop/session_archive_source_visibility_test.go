package main

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestMigratedSingleHeadArchiveDoesNotResurrectLegacyRow(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, _, head := migrationSingleDAGFixture(t)
	before, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx = t.Context()
	pinDesktopSessionRoot(t, app)
	installNoopRuntimeEvents(app)
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	installSessionCatalogForTest(t, app, filepath.Dir(path), "global", "")
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mapping, ok := state.SourceMappings[desktopSourceKey(path, head)]
	if !ok {
		t.Fatal("upgrade did not record the historical head")
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}
	if aliases := sourceAliases(state, mapping.WorkspaceID, mapping.SessionID); !slices.Contains(aliases, "path\x00"+path) {
		t.Fatalf("canonical identity is missing its displayed path alias: %q", aliases)
	}
	// Metadata-only pages identify an ordinary row by its path source key,
	// whereas the migration receipt records the selected DAG head.
	lazyAlias := "source\x00local\x00" + desktopSourceKey(path, "")
	if aliases := sourceAliases(state, mapping.WorkspaceID, mapping.SessionID); !slices.Contains(aliases, lazyAlias) {
		t.Fatalf("canonical identity cannot replace its lazy page row: %q", aliases)
	}
	assertLists := func(want int) {
		t.Helper()
		page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
		if err != nil || len(page.Items) != want {
			t.Errorf("sidebar = %+v, %v; want %d rows", page.Items, err, want)
		}
		if rows := app.listSessionsFromDir(filepath.Dir(path), ""); len(rows) != want {
			t.Errorf("history = %+v; want %d rows", rows, want)
		}
	}
	assertLists(1)
	if result, err := app.ArchiveSessionTarget(SessionSelector{SessionPath: path}); err != nil || !result.Committed {
		t.Fatalf("archive historical path = %+v, %v", result, err)
	} else if !slices.Contains(result.IdentityAliases, lazyAlias) {
		t.Fatalf("archive receipt cannot fence its lazy page row: %+v", result)
	}
	assertLists(0)
	root := app.desktopSessions.root
	app.stopSessionCatalog(time.Second)
	app.closeSessionServices()
	app = NewApp()
	app.ctx = t.Context()
	app.desktopSessions.root = root
	installNoopRuntimeEvents(app)
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	installSessionCatalogForTest(t, app, filepath.Dir(path), "global", "")
	assertLists(0)
	if result, err := app.RestoreSessionTarget(SessionSelector{Ref: &ref}); err != nil || !result.Committed {
		t.Fatalf("restore = %+v, %v", result, err)
	}
	assertLists(1)
	if after, err := desktopSourceFingerprint(path); err != nil || after != before {
		t.Fatalf("lifecycle changed retained history: %v", err)
	}
}

func TestArchivedHistoricalHeadDoesNotHideUnadoptedSibling(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, legacy, _ := migrationSingleDAGFixture(t)
	head, err := legacy.ForkHead(path, legacy.Snapshot()[1].ID, agent.HeadKindFork, "sibling")
	if err != nil {
		t.Fatal(err)
	}
	legacy.Add(provider.Message{Role: provider.RoleAssistant, Content: "sibling answer"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	before, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx = t.Context()
	pinDesktopSessionRoot(t, app)
	installNoopRuntimeEvents(app)
	t.Cleanup(app.closeSessionServices)
	installSessionCatalogForTest(t, app, filepath.Dir(path), "global", "")
	selector := SessionSelector{Source: &SessionSourceRef{HostID: localDesktopHostID, Path: path, HeadID: head}}
	if result, err := app.ArchiveSessionTarget(selector); err != nil || !result.Committed {
		t.Fatalf("archive one head = %+v, %v", result, err)
	}
	page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
	if err != nil || len(page.Items) != 1 || page.Items[0].Source == nil || page.Items[0].Source.HeadID == head {
		t.Fatalf("unadopted sidebar sibling = %+v, %v", page.Items, err)
	}
	rows := app.listSessionsFromDir(filepath.Dir(path), "")
	if len(rows) != 1 || rows[0].Source == nil || rows[0].Source.HeadID == head {
		t.Fatalf("unadopted history sibling = %+v", rows)
	}
	if result, err := app.RestoreSessionTarget(selector); err != nil || !result.Committed {
		t.Fatalf("restore one head = %+v, %v", result, err)
	}
	page, err = app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("restored heads = %+v, %v", page.Items, err)
	}
	if result, err := app.ArchiveSessionTarget(selector); err != nil || !result.Committed {
		t.Fatalf("rearchive one head = %+v, %v", result, err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mapping, found, err := state.ResolveSource(desktopSourceKey(path, head))
	if err != nil || !found {
		t.Fatalf("adopted head missing: %v", err)
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}
	aliases := sourceAliases(state, mapping.WorkspaceID, mapping.SessionID)
	if slices.Contains(aliases, "path\x00"+path) || slices.Contains(aliases, "source\x00local\x00"+desktopSourceKey(path, "")) {
		t.Fatalf("one head claimed the independent sibling's path identity: %q", aliases)
	}
	if err := app.PurgeCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	if after, err := desktopSourceFingerprint(path); err != nil || after != before {
		t.Fatalf("purge damaged independent legacy head: %v", err)
	}
	page, err = app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
	if err != nil || len(page.Items) != 1 || page.Items[0].Source == nil || page.Items[0].Source.HeadID == head {
		t.Fatalf("purge hid sibling or revived deleted head: %+v %v", page.Items, err)
	}
	state, err = app.workspaceRegistry().Load(t.Context())
	if err != nil || state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Deleted {
		t.Fatalf("head tombstone lost: %v", err)
	}
}
