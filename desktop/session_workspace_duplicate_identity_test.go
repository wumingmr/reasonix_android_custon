package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
)

// The repair removes the losing record, while a saved tab can still name it.
// That tab must be restored against the directory's surviving owner instead of
// being dropped as a stale presentation of a workspace that no longer exists.
func TestSavedTabNamingAbsorbedWorkspaceIsRestored(t *testing.T) {
	app := newSavedTabReconcileTestApp(t)
	finishSavedTabMigration(app)
	root, err := ensureGlobalWorkspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	// Registered while Global has no record: the project record takes the
	// directory, the way a build without physical identity left it.
	ref, duplicate := createLegacyCleanupSession(t, app, root, "kept-session", true)
	if duplicate == workspacestate.GlobalWorkspaceID {
		t.Fatalf("fixture did not register a separate project record: %q", duplicate)
	}
	path := config.DesktopWorkspaceStatePath()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	document["workspaceIds"] = append(document["workspaceIds"].([]any), workspacestate.GlobalWorkspaceID)
	document["workspaces"].(map[string]any)[workspacestate.GlobalWorkspaceID] = map[string]any{
		"id": workspacestate.GlobalWorkspaceID, "root": root,
		"title": "Global", "visible": true, "sessionIds": []string{},
	}
	if body, err = json.Marshal(document); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	if owner, err := app.ensureDesktopWorkspace(t.Context(), "global", ""); err != nil || owner != workspacestate.GlobalWorkspaceID {
		t.Fatalf("repair resolved %q: %v", owner, err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, stranded := state.Workspaces[duplicate]; stranded || !slices.Contains(state.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs, ref.SessionID) {
		t.Fatalf("session did not move to the surviving owner: %+v", state.Workspaces)
	}
	file := desktopTabsFile{Tabs: []desktopTabEntry{savedProjectTab("kept", ref.SessionID, "", root, duplicate)}, ActiveTab: "kept"}
	got, _ := app.reconcileSavedTabs(t.Context(), file)
	if len(got.Tabs) != 1 || got.Tabs[0].SessionID != ref.SessionID {
		t.Fatalf("saved tab naming the absorbed workspace was dropped: %+v", got.Tabs)
	}
}

// A registry written before physical workspace identity could hold both the
// Global folder and an ordinary project for that same directory. Every sidebar
// projection over it resolves an owner, so an unrepaired duplicate strands the
// Global folder and the project together.
func TestProjectTopicsRecoverFromDuplicateGlobalWorkspaceIdentity(t *testing.T) {
	isolateDesktopUserDirs(t)
	root, err := ensureGlobalWorkspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	duplicate := desktopWorkspaceID("project", root)
	path := config.DesktopWorkspaceStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"version": 3, "initialized": true, "sessionStates": map[string]any{},
		"workspaceIds": []string{workspacestate.GlobalWorkspaceID, duplicate},
		"workspaces": map[string]any{
			workspacestate.GlobalWorkspaceID: map[string]any{
				"id": workspacestate.GlobalWorkspaceID, "root": root,
				"title": "Global", "visible": true, "sessionIds": []string{},
			},
			duplicate: map[string]any{
				"id": duplicate, "root": root,
				"title": "global-workspace", "visible": true, "sessionIds": []string{},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx = t.Context()
	t.Cleanup(app.closeSessionServices)
	for _, req := range []ProjectTopicPageRequest{
		{Scope: "global", Limit: 50},
		{Scope: "project", WorkspaceRoot: root, Limit: 50},
	} {
		if _, err := app.ListProjectTopics(req); err != nil {
			t.Fatalf("list %s topics: %v", req.Scope, err)
		}
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, stranded := state.Workspaces[duplicate]; stranded || len(state.Workspaces) != 1 {
		t.Fatalf("duplicate directory owner survived: %+v", state.Workspaces)
	}
	global := state.Workspaces[workspacestate.GlobalWorkspaceID]
	if global.Title != "Global" || !global.Visible {
		t.Fatalf("global lost its presentation: %+v", global)
	}
}
