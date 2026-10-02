package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/sessioncatalog"
)

func TestOrganizationAdmissionRejectsAdoptionAfterResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	registry := workspacestate.NewStore(path)
	ctx := t.Context()
	id := workspacestate.GlobalWorkspaceID
	if err := registry.EnsureWorkspace(ctx, workspacestate.Workspace{ID: id}); err != nil {
		t.Fatal(err)
	}
	if err := registry.AttachSession(ctx, "", id, "canonical", ""); err != nil {
		t.Fatal(err)
	}
	o, _, err := registry.UpdateOrganization(ctx, id, nil, func(o *workspacestate.Organization) error {
		return applyOrganizationMutation(o, SessionOrganizationMutation{Kind: "create-group", GroupID: "g", Title: "Group"}, "", "")
	})
	if err != nil {
		t.Fatal(err)
	}
	// Force the ordering without timing hooks: resolve an as-yet unadopted
	// source, commit its adoption, then enter the group mutation transaction.
	source := &SessionSourceRef{HostID: localDesktopHostID, Path: filepath.Join(t.TempDir(), "old.jsonl")}
	source.SourceKey = desktopSourceKey(source.Path, "")
	target := SessionTarget{Source: source, SessionPath: source.Path}
	if err := registry.RecordSource(ctx, workspacestate.SourceMapping{SourceKey: source.SourceKey, Path: source.Path,
		WorkspaceID: id, SessionID: "canonical", Fingerprint: "verified-source"}, workspacestate.Presentation{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, applied, err := registry.UpdateOrganizationWithState(ctx, id, &o.Revision, func(state *workspacestate.State, o *workspacestate.Organization) error {
		return applyResolvedOrganizationMutation(state, id, o, []SessionTarget{target},
			SessionOrganizationMutation{Kind: "set-group", GroupID: "g"}, "source\x00local\x00"+source.SourceKey, "")
	})
	if !errors.Is(err, workspacestate.ErrMutationConflict) || applied {
		t.Fatalf("adopted source admitted: applied=%v err=%v", applied, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected mutation changed authoritative registry: %v", err)
	}
}

func TestGroupMutationsAdmitOnlyExplicitSourceAndPreserveUngrouping(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.ctx = t.Context()
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	catalog, err := sessioncatalog.Open(t.Context(), sessioncatalog.Options{Path: filepath.Join(t.TempDir(), "catalog.sqlite"), MetadataOnly: true, StartPaused: true})
	if err != nil {
		t.Fatal(err)
	}
	app.sessionCatalog.Store(catalog)
	t.Cleanup(func() { app.desktopSessions.readSnapshots.close(); app.stopSessionCatalog(time.Second) })
	paths := []string{filepath.Join(dir, "selected.jsonl"), filepath.Join(dir, "unrelated.jsonl")}
	for i, path := range paths {
		// Metadata identifies the source. Group commands need no transcript
		// parsing or execution recovery, even when bodies cannot be decoded.
		if err := os.WriteFile(path, []byte("unread authoritative body\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := agent.SaveBranchMeta(path, agent.BranchMeta{Scope: "global", TopicID: "shared"}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.UpsertSession(t.Context(), sessioncatalog.SessionRecord{Path: path, Directory: dir, Scope: "global",
			TopicID: "shared", TopicTitle: "Shared", LastActivityAt: int64(i + 1), OrdinaryVisible: true, Health: sessioncatalog.HealthOK}); err != nil {
			t.Fatal(err)
		}
	}
	workspace := SessionOrganizationWorkspace{Scope: "global"}
	organization, err := app.GetSessionOrganization(workspace)
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(m SessionOrganizationMutation) {
		t.Helper()
		organization, err = app.UpdateSessionOrganization(workspace, organization.Revision, m)
		if err != nil || !organization.Applied {
			t.Fatalf("mutation %+v: %+v %v", m, organization, err)
		}
	}
	mutate(SessionOrganizationMutation{Kind: "create-group", GroupID: "g", Title: "Group"})
	load := func() workspacestate.Organization {
		t.Helper()
		state, err := app.workspaceRegistry().Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return *state.Workspaces[desktopWorkspaceID("global", "")].Organization
	}
	if got := load(); len(got.Imported) != 0 || len(got.Order) != 0 {
		t.Fatalf("group creation enumerated unrelated sources: %+v", got)
	}
	mutate(SessionOrganizationMutation{Kind: "set-group", GroupID: "g", Target: &SessionSelector{SessionPath: paths[0]}})
	key := "source\x00local\x00" + desktopSourceKey(paths[0], "")
	if got := load(); len(got.Imported) != 1 || !got.Imported[key] || !reflect.DeepEqual(got.Groups[0].Members, []string{key}) {
		t.Fatalf("path selector did not admit the exact physical source: %+v", got)
	}
	stale, err := app.UpdateSessionOrganization(workspace, organization.Revision-1, SessionOrganizationMutation{Kind: "set-group", GroupID: "g", Target: &SessionSelector{SessionPath: paths[1]}})
	if err != nil || stale.Applied || len(load().Imported) != 1 {
		t.Fatalf("stale CAS admitted a new source: %+v %v", stale, err)
	}
	_, err = app.UpdateSessionOrganization(workspace, organization.Revision, SessionOrganizationMutation{Kind: "set-group", GroupID: "g",
		Target: &SessionSelector{SessionPath: filepath.Join(dir, "missing.jsonl")}})
	if err == nil || len(load().Imported) != 1 {
		t.Fatal("missing source entered organization")
	}
	mutate(SessionOrganizationMutation{Kind: "rename-group", GroupID: "g", Title: "Renamed"})
	if len(load().Imported) != 1 {
		t.Fatal("rename enumerated unrelated sources")
	}
	mutate(SessionOrganizationMutation{Kind: "set-group", Target: &SessionSelector{Source: &SessionSourceRef{HostID: localDesktopHostID, Path: paths[0]}}})
	if got := load(); !got.Imported[key] || len(got.Groups[0].Members) != 0 {
		t.Fatalf("explicit ungrouping lost its import fence: %+v", got)
	}
	// Discovering an old topic-wide assignment later must not undo the user's
	// explicit choice. The unrelated source can still inherit that preference.
	if err := updateProjectsFile(func(f *desktopProjectFile) (bool, error) {
		f.GlobalGroups = []desktopGroup{{ID: "g", Title: "Old title", TopicIDs: []string{"shared"}}}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.GetSessionOrganization(workspace); err != nil {
		t.Fatal(err)
	}
	got := load()
	unrelated := "source\x00local\x00" + desktopSourceKey(paths[1], "")
	if got.Groups[0].Title != "Renamed" || !reflect.DeepEqual(got.Groups[0].Members, []string{unrelated}) || !got.Imported[key] {
		t.Fatalf("late preference import overwrote an explicit choice: %+v", got)
	}
}
