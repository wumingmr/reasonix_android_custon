package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

func legacyTabScopeFixture(t *testing.T, register bool) (app *App, projectRoot, legacyPath string) {
	t.Helper()
	isolateDesktopUserDirs(t)
	projectRoot = robustTempDir(t)
	if register {
		if err := addProject(projectRoot, "Legacy project"); err != nil {
			t.Fatal(err)
		}
	}
	dir := desktopSessionDir(projectRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyPath = filepath.Join(dir, "legacy.jsonl")
	legacy := agent.NewSession("system")
	legacy.Add(provider.Message{ID: "user", Role: provider.RoleUser, Content: "legacy content"})
	if err := legacy.Save(legacyPath); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(legacyPath, agent.BranchMeta{Scope: "project", WorkspaceRoot: projectRoot}); err != nil {
		t.Fatal(err)
	}
	entry := desktopTabEntry{ID: "tab", Scope: "global", TopicID: "topic", SessionPath: legacyPath}
	file := desktopTabsFile{Tabs: []desktopTabEntry{entry}, ActiveTab: entry.ID, TabOrder: []string{entry.ID}}
	if err := os.MkdirAll(desktopConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(desktopConfigDir(), tabsFileName), mustMarshalJSON(t, file), 0o600); err != nil {
		t.Fatal(err)
	}
	app = NewApp()
	t.Cleanup(app.closeSessionServices)
	app.desktopSessions.root = filepath.Join(t.TempDir(), "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(t.TempDir(), "workspace-state-v1.json"))
	return app, projectRoot, legacyPath
}

func TestTabDerivedLegacySourceTakesScopeFromProjectDirectory(t *testing.T) {
	app, projectRoot, legacyPath := legacyTabScopeFixture(t, true)
	_, legacy := app.desktopHistoricalRoots()
	source, ok := legacy[canonicalRuntimeRoot(filepath.Dir(legacyPath))]
	if !ok {
		t.Fatalf("project session directory not discovered: %#v", legacy)
	}
	if source.scope != "project" || !sameDesktopPath(source.workspaceRoot, projectRoot) {
		t.Fatalf("source scope = %q root = %q, want project %q", source.scope, source.workspaceRoot, projectRoot)
	}
	if err := app.migrateLegacyDirectory(t.Context(), source); errors.Is(err, errSessionWorkspaceConflict) {
		t.Fatalf("session of a registered project conflicted: %v", err)
	} else if err != nil {
		t.Fatal(err)
	}
}

func TestTabDerivedLegacySourceOfUnregisteredProjectMigratesOnce(t *testing.T) {
	app, projectRoot, legacyPath := legacyTabScopeFixture(t, false)
	before, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	_, legacy := app.desktopHistoricalRoots()
	source := legacy[canonicalRuntimeRoot(filepath.Dir(legacyPath))]
	if source.scope != "project" || !sameDesktopPath(source.workspaceRoot, projectRoot) {
		t.Fatalf("source scope = %q root = %q, want project %q", source.scope, source.workspaceRoot, projectRoot)
	}
	for range 2 {
		if err := app.migrateLegacyDirectory(t.Context(), source); err != nil {
			t.Fatalf("migration of an unregistered project's session: %v", err)
		}
	}
	state, err := app.desktopSessions.workspaceState.Load(t.Context())
	if err != nil || len(state.SourceMappings) != 1 {
		t.Fatalf("re-run duplicated or lost the target: %+v, %v", state.SourceMappings, err)
	}
	after, err := os.ReadFile(legacyPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("legacy transcript changed: %v", err)
	}
}

func TestTabDerivedLegacySourceStillReportsGenuineWorkspaceConflict(t *testing.T) {
	for _, register := range []bool{true, false} {
		app, _, legacyPath := legacyTabScopeFixture(t, register)
		elsewhere := robustTempDir(t)
		if err := agent.SaveBranchMetaPreserveUpdated(legacyPath, agent.BranchMeta{Scope: "project", WorkspaceRoot: elsewhere}); err != nil {
			t.Fatal(err)
		}
		_, legacy := app.desktopHistoricalRoots()
		source := legacy[canonicalRuntimeRoot(filepath.Dir(legacyPath))]
		if err := app.migrateLegacyDirectory(t.Context(), source); !errors.Is(err, errSessionWorkspaceConflict) {
			t.Fatalf("registered=%v: err = %v, want workspace conflict", register, err)
		}
	}
}

func TestHistoricalImportRerunRetriesFailedSourceOnce(t *testing.T) {
	app, projectRoot, legacyPath := legacyTabScopeFixture(t, true)
	app.ctx = t.Context()
	installNoopRuntimeEvents(app)
	t.Cleanup(app.stopHistoricalImports)
	elsewhere := robustTempDir(t)
	if err := agent.SaveBranchMetaPreserveUpdated(legacyPath, agent.BranchMeta{Scope: "project", WorkspaceRoot: elsewhere}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ListHistoricalSessions(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.StartHistoricalImport(nil); err != nil {
		t.Fatal(err)
	}
	status := awaitHistoricalBatch(t, app)
	if len(status.Items) != 1 || status.Items[0].Status != "failed" || status.Items[0].ErrorCode != "workspace_conflict" {
		t.Fatalf("first run did not fail with the conflict: %+v", status)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(legacyPath, agent.BranchMeta{Scope: "project", WorkspaceRoot: projectRoot}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := app.StartHistoricalImport(nil); err != nil {
			t.Fatal(err)
		}
		status = awaitHistoricalBatch(t, app)
	}
	if len(status.Items) != 1 || status.Items[0].Status != "imported" {
		t.Fatalf("failed source was not attempted again: %+v", status)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil || len(state.SourceMappings) != 1 {
		t.Fatalf("re-import duplicated the target: %+v, %v", state.SourceMappings, err)
	}
}
