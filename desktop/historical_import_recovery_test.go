package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

func TestHistoricalImportRetrySettlesWorkspaceConflict(t *testing.T) {
	app, projectRoot, path := historicalRecoveryFixture(t)
	app.ctx = t.Context()
	installNoopRuntimeEvents(app)
	t.Cleanup(app.stopHistoricalImports)
	elsewhere := historicalRecoveryGitRoot(t, robustTempDir(t))
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{Scope: "project", WorkspaceRoot: elsewhere}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ListHistoricalSessions(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.StartHistoricalImport(nil); err != nil {
		t.Fatal(err)
	}
	first := awaitHistoricalBatch(t, app)
	if len(first.Items) != 1 || first.Items[0].Status != "failed" || first.Items[0].ErrorCode != "workspace_conflict" {
		t.Fatalf("fixture must create actual conflict: %+v", first)
	}
	before, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(before.RecoveryEntries) != 1 {
		t.Fatalf("conflict did not create one recovery entry: %+v", before.RecoveryEntries)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{Scope: "project", WorkspaceRoot: projectRoot}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.StartHistoricalImport(nil); err != nil {
		t.Fatal(err)
	}
	after := awaitHistoricalBatch(t, app)
	if len(after.Items) != 1 || after.Items[0].Status != "imported" {
		t.Fatalf("retry failed: %+v", after)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	histories := v5MigrationHistories(t, app)
	if len(histories) != 1 {
		t.Fatalf("retry did not produce readable history: %+v", histories)
	}
	pending := 0
	for _, entry := range state.RecoveryEntries {
		if entry.Status == "pending" {
			pending++
		}
	}
	t.Logf("actual workspace_conflict -> repaired metadata -> imported + readable; recovery entries still pending=%d", pending)
	if pending != 0 {
		t.Errorf("successful retry left %d stale pending recovery entries", pending)
	}
}

func TestHistoricalImportReceiptReplaySettlesWorkspaceConflict(t *testing.T) {
	testHistoricalImportReceiptReplay(t, workspacestate.Active, false, false)
}

func TestHistoricalImportArchivedReceiptReplaySettlesWorkspaceConflict(t *testing.T) {
	for _, test := range []struct {
		name                         string
		removeMapping, missingTarget bool
	}{
		{name: "existing-mapping"}, {name: "repaired-mapping", removeMapping: true}, {name: "unreadable-target", missingTarget: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			testHistoricalImportReceiptReplay(t, workspacestate.Archived, test.removeMapping, test.missingTarget)
		})
	}
}

func TestHistoricalImportDeletedReceiptReplayPreservesRecovery(t *testing.T) {
	testHistoricalImportReceiptReplay(t, workspacestate.Deleted, false, false)
}

func testHistoricalImportReceiptReplay(t *testing.T, lifecycle string, removeMapping, missingTarget bool) {
	t.Helper()
	app, projectRoot, path := historicalRecoveryFixture(t)
	source := desktopMigrationSource{scope: "project", workspaceRoot: projectRoot}
	if err := app.migrateLegacySession(t.Context(), path, source, ""); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.SourceMappings) != 1 {
		t.Fatalf("fixture did not publish exactly one source: %+v", state.SourceMappings)
	}
	var mapping workspacestate.SourceMapping
	for _, imported := range state.SourceMappings {
		mapping = imported
	}
	if got := v5MigrationHistories(t, app); len(got) != 1 {
		t.Fatalf("fixture did not import readable history: %+v", got)
	}
	// Simulate a receipt written by an earlier build, which imported successfully
	// but left its exact source-version conflict pending.
	if err := app.sourceRecovery(t.Context(), path, "legacy", "workspace_conflict", "project", projectRoot, mapping.HeadID); err != nil {
		t.Fatal(err)
	}
	if lifecycle != workspacestate.Active {
		if err := app.workspaceRegistry().ArchiveSession(t.Context(), mapping.SessionID); err != nil {
			t.Fatal(err)
		}
	}
	state, err = app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle == workspacestate.Deleted {
		if err := app.workspaceRegistry().BeginPurge(t.Context(), mapping.SessionID, state.Generation); err != nil {
			t.Fatal(err)
		}
	}
	if removeMapping {
		delete(state.SourceMappings, mapping.SourceKey)
		body, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(app.workspaceRegistry().Path(), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if missingTarget {
		app.closeSessionServices()
		if err := os.Rename(filepath.Join(app.desktopSessions.root, mapping.SessionID), filepath.Join(t.TempDir(), "unavailable-target")); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		err := app.migrateLegacySession(t.Context(), path, source, "")
		if missingTarget && err == nil {
			t.Fatal("unreadable archived target accepted")
		}
		if !missingTarget && err != nil {
			t.Fatal(err)
		}
		state, err := app.workspaceRegistry().Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(state.RecoveryEntries) != 1 {
			t.Fatalf("unexpected recovery entries: %+v", state.RecoveryEntries)
		}
		for _, entry := range state.RecoveryEntries {
			wantStatus, wantTarget := "restored", mapping.SessionID
			if lifecycle == workspacestate.Deleted || missingTarget {
				wantStatus, wantTarget = "pending", ""
			}
			if entry.Status != wantStatus || entry.SessionID != wantTarget {
				t.Fatalf("completed receipt left stale recovery: %+v", entry)
			}
		}
		if got := state.SessionStates[mapping.SessionID].Lifecycle; got != lifecycle {
			t.Fatalf("replay changed lifecycle: %s", got)
		}
		// BeginPurge only tombstones; physical removal belongs to the purge
		// worker. Its recovery must remain pending without reviving lifecycle.
		if !missingTarget && lifecycle != workspacestate.Deleted {
			if got := v5MigrationHistories(t, app); len(got) != 1 {
				t.Fatalf("receipt replay duplicated or lost history: %+v", got)
			}
		}
	}
}

func TestHistoricalImportPartialRetryPreservesOtherConflict(t *testing.T) {
	app, firstRoot, firstPath := historicalRecoveryFixture(t)
	secondRoot, secondPath := historicalRecoveryLegacySource(t)
	elsewhere := historicalRecoveryGitRoot(t, robustTempDir(t))
	app.ctx = t.Context()
	installNoopRuntimeEvents(app)
	t.Cleanup(app.stopHistoricalImports)
	for _, path := range []string{firstPath, secondPath} {
		if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{Scope: "project", WorkspaceRoot: elsewhere}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.ListHistoricalSessions(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.StartHistoricalImport(nil); err != nil {
		t.Fatal(err)
	}
	first := awaitHistoricalBatch(t, app)
	if len(first.Items) != 2 || first.Failed != 2 {
		t.Fatalf("expected two real conflicts: %+v", first)
	}
	for index, repair := range []struct{ root, path string }{{firstRoot, firstPath}, {secondRoot, secondPath}} {
		if err := agent.SaveBranchMetaPreserveUpdated(repair.path, agent.BranchMeta{Scope: "project", WorkspaceRoot: repair.root}); err != nil {
			t.Fatal(err)
		}
		if _, err := app.StartHistoricalImport(nil); err != nil {
			t.Fatal(err)
		}
		batch := awaitHistoricalBatch(t, app)
		if batch.Failed != 1-index {
			t.Fatalf("retry failed unexpected sources: %+v", batch)
		}
		state, err := app.workspaceRegistry().Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		pending := 0
		for _, entry := range state.RecoveryEntries {
			if entry.Status == "pending" {
				pending++
				if entry.SourceKey != desktopSourceKey(secondPath, "") {
					t.Fatalf("wrong conflict remains pending: %+v", entry)
				}
			}
		}
		if pending != 1-index {
			t.Fatalf("retry settled an unresolved sibling or kept repaired conflict: pending=%d", pending)
		}
		if got := v5MigrationHistories(t, app); len(got) != index+1 {
			t.Fatalf("partial retry histories: %+v", got)
		}
	}
	if _, err := app.StartHistoricalImport(nil); err != nil {
		t.Fatal(err)
	}
	if batch := awaitHistoricalBatch(t, app); batch.Failed != 0 {
		t.Fatalf("repeat failed: %+v", batch)
	}
	if got := v5MigrationHistories(t, app); len(got) != 2 {
		t.Fatalf("repeat duplicated or lost history: %+v", got)
	}
}

// Use independent repositories so a runner's enclosing worktree cannot fold
// unrelated fixture projects or source directories into the same identity.
func historicalRecoveryGitRoot(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "--quiet", dir).CombinedOutput(); err != nil {
		t.Fatalf("initialize fixture repository: %v: %s", err, out)
	}
	if !sameDesktopPath(canonicalRuntimeRoot(dir), dir) {
		t.Fatalf("fixture root collapsed: %q -> %q", dir, canonicalRuntimeRoot(dir))
	}
	return dir
}

func historicalRecoveryFixture(t *testing.T) (*App, string, string) {
	t.Helper()
	isolateDesktopUserDirs(t)
	historicalRecoveryGitRoot(t, globalWorkspaceRoot())
	projectRoot, path := historicalRecoveryLegacySource(t)
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.desktopSessions.root = historicalRecoveryGitRoot(t, filepath.Join(t.TempDir(), "by-id"))
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(t.TempDir(), "workspace-state-v1.json"))
	return app, projectRoot, path
}

func historicalRecoveryLegacySource(t *testing.T) (string, string) {
	t.Helper()
	projectRoot := historicalRecoveryGitRoot(t, robustTempDir(t))
	if err := addProject(projectRoot, "Legacy project"); err != nil {
		t.Fatal(err)
	}
	dir := historicalRecoveryGitRoot(t, desktopSessionDir(projectRoot))
	path := filepath.Join(dir, "legacy.jsonl")
	legacy := agent.NewSession("system")
	legacy.Add(provider.Message{ID: "user", Role: provider.RoleUser, Content: "legacy content"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{Scope: "project", WorkspaceRoot: projectRoot}); err != nil {
		t.Fatal(err)
	}
	return projectRoot, path
}

func TestHistoricalReviewedImportRetrySettlesBaseRecovery(t *testing.T) {
	app, root, path := historicalRecoveryFixture(t)
	app.ctx = t.Context()
	installNoopRuntimeEvents(app)
	t.Cleanup(app.stopHistoricalImports)
	listed, err := app.ListHistoricalSessions()
	if err != nil || len(listed.Items) != 1 {
		t.Fatalf("list: %+v %v", listed, err)
	}
	base, err := app.ImportHistoricalSession(listed.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	id, source, err := app.historicalSourceForSelector(SessionSelector{Ref: &base.Session})
	if err != nil {
		t.Fatal(err)
	}
	update := app.checkHistoricalSourceUpdate(t.Context(), id, source)
	if update.Status != "available" || update.Source == nil {
		t.Fatalf("updated source: %+v", update)
	}
	elsewhere := historicalRecoveryGitRoot(t, robustTempDir(t))
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{Scope: "project", WorkspaceRoot: elsewhere}); err != nil {
		t.Fatal(err)
	}
	prepare := func() (SessionPreparationView, SessionRestoreResult, error) {
		prepared, err := app.PrepareHistoricalSourceVersion(*update.Source, update.Version)
		if err != nil {
			return prepared, SessionRestoreResult{}, err
		}
		app.historicalImports.mu.Lock()
		call := app.historicalImports.operations[prepared.OperationID]
		app.historicalImports.mu.Unlock()
		result, err := waitHistoricalImport(call)
		return prepared, result, err
	}
	if _, _, err := prepare(); err == nil {
		t.Fatal("reviewed source with conflicting workspace was accepted")
	}
	failed, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(failed.RecoveryEntries) != 1 {
		t.Fatalf("missing actual conflict: %+v", failed.RecoveryEntries)
	}
	for _, entry := range failed.RecoveryEntries {
		if entry.Reason != "workspace_conflict" || entry.Status != "pending" || entry.SourceKey != id || entry.Fingerprint != update.Version {
			t.Fatalf("wrong conflict: %+v", entry)
		}
	}
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{Scope: "project", WorkspaceRoot: root}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, result, err := prepare()
		if err != nil {
			t.Fatal(err)
		}
		if result.Session.SessionID == base.Session.SessionID {
			t.Fatal("reviewed version reused original adoption")
		}
		state, err := app.workspaceRegistry().Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(state.SourceMappings[id], original.SourceMappings[id]) {
			t.Fatal("review import changed original adoption")
		}
		if got := state.SourceMappings[id+":review:"+update.Version]; got.SessionID != result.Session.SessionID {
			t.Fatalf("review mapping missing: %+v", got)
		}
		for _, entry := range state.RecoveryEntries {
			if entry.Status != "restored" || entry.SessionID != result.Session.SessionID {
				t.Fatalf("reviewed retry left base recovery pending: %+v", entry)
			}
		}
		if got := v5MigrationHistories(t, app); len(got) != 2 {
			t.Fatalf("reviewed retry duplicated or lost history: %+v", got)
		}
	}
}
