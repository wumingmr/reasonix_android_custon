package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/identitylock"
	"reasonix/internal/session"
)

func TestHistoricalRegressionContentReadySurvivesSourceMove(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	const id = "review-content-ready"
	old := coldV4MigrationFixture(t, root, id)
	app := newHistoricalLifecycleApp(t)
	sourceID := historicalLifecycleID(t, app, id)
	workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, id)
	fingerprint, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	opID, err := app.prepareDesktopImport(t.Context(), desktopMigrationSource{scope: "global"}, path, fingerprint, id, workspace)
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "bundle")
	if err := old.Export(t.Context(), session.SessionRef{HostID: "migration-source", SessionID: id}, bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := app.desktopSessionService("").ImportWithHeader(t.Context(), bundle, session.CreateOptions{SessionID: id, CWD: globalWorkspaceRoot(), Origin: session.SessionOriginCanonicalImport}); err != nil {
		t.Fatal(err)
	}
	if err := app.commitDesktopImport(t.Context(), desktopMigrationSource{scope: "global", operationID: opID, deferArchive: true}, path, "canonical", fingerprint, id, workspace); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(t.TempDir(), "moved-source")); err != nil {
		t.Fatal(err)
	}
	app.stopHistoricalImports()
	app.closeSessionServices()
	app = newHistoricalLifecycleApp(t)
	release, err := identitylock.Acquire(t.Context(), filepath.Join(root, "."+id+".ownership.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := app.ImportHistoricalSession(sourceID); err != nil {
		t.Fatalf("valid content_ready target cannot recover after source moved: %v", err)
	}
}

func TestHistoricalRegressionColdV4VisibleInNormalLists(t *testing.T) {
	isolateDesktopUserDirs(t)
	coldV4MigrationFixture(t, config.SessionStoreDir(), "review-visible")
	app := newHistoricalLifecycleApp(t)
	installSessionCatalogForTest(t, app, config.SessionDir(), "global", "")
	management, err := app.ListHistoricalSessions()
	if err != nil || len(management.Items) != 1 {
		t.Fatalf("fixture missing from historical management: %+v %v", management, err)
	}
	page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Errorf("cold v4 source absent from sidebar: %+v", page.Items)
	}
	rows := app.listSessionsFromDir(config.SessionDir(), "")
	if len(rows) != 1 {
		t.Errorf("cold v4 source absent from history: %+v", rows)
	}
	if len(page.Items) != 1 {
		return
	}
	selector := SessionSelector{Source: page.Items[0].Source}
	if _, err := app.RenameSessionTarget(selector, "Renamed historical source"); err != nil {
		t.Fatal(err)
	}
	if err := app.SetSessionPinned(selector, true); err != nil {
		t.Fatal(err)
	}
	shells := app.mergeCanonicalWorkspaceShells([]ProjectNode{{Kind: "global_folder", Key: "global_folder"}})
	if len(shells) != 1 || len(shells[0].Children) != 1 || !shells[0].Children[0].Pinned {
		t.Fatalf("source-only workspace lost its pinned shell: %+v", shells)
	}
	filtered, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Query: "Renamed historical", Limit: 1})
	if err != nil || len(filtered.Items) != 1 || !filtered.Items[0].Pinned {
		t.Fatalf("display overrides missing from search: %+v %v", filtered, err)
	}
	prepared, err := app.PrepareSession(selector)
	if err != nil {
		t.Fatal(err)
	}
	app.historicalImports.mu.Lock()
	call := app.historicalImports.operations[prepared.OperationID]
	app.historicalImports.mu.Unlock()
	result, err := waitHistoricalImport(call)
	if err != nil {
		t.Fatal(err)
	}
	page, err = app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
	if err != nil || len(page.Items) != 1 || page.Items[0].Session == nil || !page.Items[0].Pinned {
		t.Fatalf("adoption duplicated source or lost pin: %+v %v", page, err)
	}
	if err := app.ArchiveCanonicalSession(result.Session); err != nil {
		t.Fatal(err)
	}
	page, err = app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("retained source revived after archive: %+v %v", page, err)
	}
}

func TestHistoricalRegressionPinHistoricalSourceImmediatelyVisible(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, _, _ := migrationSingleDAGFixture(t)
	app := newHistoricalLifecycleApp(t)
	installSessionCatalogForTest(t, app, filepath.Dir(path), "global", "")
	req := ProjectTopicPageRequest{Scope: "global", Limit: 50}
	page, err := app.ListProjectTopics(req)
	if err != nil || len(page.Items) != 1 || page.Items[0].Source == nil {
		t.Fatalf("fixture: %+v %v", page, err)
	}
	if err := app.SetSessionPinned(SessionSelector{Source: page.Items[0].Source}, true); err != nil {
		t.Fatal(err)
	}
	page, err = app.ListProjectTopics(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.Items[0].Pinned {
		t.Fatalf("successful pin not projected before preparation: %+v", page.Items)
	}
}

func TestHistoricalRegressionStartupPublishesScopedCatalogWithoutImport(t *testing.T) {
	isolateDesktopUserDirs(t)
	workspace := t.TempDir()
	if err := saveProjectsFile(desktopProjectFile{Projects: []desktopProject{{Root: workspace}}}); err != nil {
		t.Fatal(err)
	}
	coldV4MigrationFixture(t, config.SessionStoreDir(), "global-source")
	coldV4MigrationFixture(t, config.ProjectSessionStoreDir(workspace), "project-source")
	app := newHistoricalLifecycleApp(t)
	release, err := identitylock.Acquire(t.Context(), filepath.Join(config.SessionStoreDir(), ".global-source.ownership.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	runHistoryDiscoveryStartup(t, app)
	app.ctx = t.Context() // The startup helper cancels its watchdog context on return.
	// No management RPC or explicit preparation has been called.
	for _, scope := range []string{"global", "project"} {
		root := ""
		if scope == "project" {
			root = workspace
		}
		page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: scope, WorkspaceRoot: root, Limit: 50})
		if err != nil || len(page.Items) != 1 || !page.Items[0].Historical || page.Items[0].Label != scope+"-source" {
			t.Fatalf("startup catalog for %s: %+v %v", scope, page, err)
		}
	}
	if rows := app.listSessionsFromDir(t.TempDir(), ""); len(rows) != 0 {
		t.Fatalf("unknown directory leaked global sources: %+v", rows)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil || len(state.SourceMappings) != 0 || len(state.PendingOperations) != 0 {
		t.Fatalf("discovery started a conversion: %+v %v", state, err)
	}
}

func TestHistoricalRegressionShutdownPreservesPendingBatch(t *testing.T) {
	isolateDesktopUserDirs(t)
	coldV4MigrationFixture(t, config.SessionStoreDir(), "review-first")
	coldV4MigrationFixture(t, config.SessionStoreDir(), "review-second")
	app := newHistoricalLifecycleApp(t)
	first := historicalLifecycleID(t, app, "review-first")
	second := historicalLifecycleID(t, app, "review-second")
	entered, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(proceed) }) }
	t.Cleanup(release)
	app.desktopSessions.beforeMigrationRegistryCommit = func() error { close(entered); <-proceed; return nil }
	if _, err := app.StartHistoricalImport([]string{first, second}); err != nil {
		t.Fatal(err)
	}
	<-entered
	stopped := make(chan struct{})
	go func() { app.stopHistoricalImports(); close(stopped) }()
	<-app.historicalImports.ctx.Done()
	release()
	<-stopped
	awaitHistoricalBatch(t, app)
	data, err := os.ReadFile(historicalImportQueuePath())
	if err != nil {
		t.Fatal(err)
	}
	var saved historicalImportQueueSidecar
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Queue) != 1 || saved.Queue[0] != second {
		t.Fatalf("shutdown lost pending second selection: current=%q queue=%v", saved.Current, saved.Queue)
	}
	app.closeSessionServices()
	restarted := newHistoricalLifecycleApp(t)
	status, err := restarted.ListHistoricalSessions()
	if err != nil || !status.Paused || status.Running || status.Remaining != 2 {
		t.Fatalf("restart did not retain paused batch: %+v %v", status, err)
	}
	if _, err := restarted.ControlHistoricalImport("resume"); err != nil {
		t.Fatal(err)
	}
	status = awaitHistoricalBatch(t, restarted)
	if status.Completed != 2 || status.Remaining != 0 {
		t.Fatalf("manual continuation failed: %+v", status)
	}
}

func TestHistoricalRegressionShutdownDrainsStartupWorker(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := newHistoricalLifecycleApp(t)
	c := &app.historicalImports
	entered, proceed := make(chan struct{}), make(chan struct{})
	var once, enterOnce sync.Once
	release := func() { once.Do(func() { close(proceed) }) }
	t.Cleanup(release)
	// The startup worker and the catalog discovery it starts both publish a
	// tree change; either may arrive first, and both are workers stop drains.
	app.projectTreeChangedHook = func() { enterOnce.Do(func() { close(entered) }); <-proceed }
	app.startDesktopSessionMigration(t.Context())
	<-app.desktopMigrationDone
	app.markTabsRestored()
	<-entered
	stopped := make(chan struct{})
	go func() { app.stopHistoricalImports(); close(stopped) }()
	<-c.ctx.Done()
	select {
	case <-stopped:
		t.Fatal("shutdown did not drain the startup worker")
	default:
	}
	release()
	<-stopped
	select {
	case <-app.desktopMigrationDone:
	default:
		t.Fatal("startup recovery outlived the coordinator")
	}
}

func TestHistoricalRegressionTwoInstancesPreservePresentationWrites(t *testing.T) {
	isolateDesktopUserDirs(t)
	a, b := newHistoricalLifecycleApp(t), newHistoricalLifecycleApp(t)
	if _, err := a.ListHistoricalSessions(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ListHistoricalSessions(); err != nil {
		t.Fatal(err)
	}
	if err := a.saveHistoricalSourcePresentation("source-a", func(p *historicalSourcePresentation) { p.Title = "title-a" }); err != nil {
		t.Fatal(err)
	}
	if err := b.saveHistoricalSourcePresentation("source-b", func(p *historicalSourcePresentation) { p.Title = "title-b" }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(historicalImportQueuePath())
	if err != nil {
		t.Fatal(err)
	}
	var saved historicalImportQueueSidecar
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Presentations["source-a"].Title != "title-a" {
		t.Fatalf("second Desktop erased first title: %+v", saved.Presentations)
	}
}

func TestHistoricalRegressionUpdateCheckRejectsActiveWriter(t *testing.T) {
	isolateDesktopUserDirs(t)
	const id = "review-writer"
	old := coldV4MigrationFixture(t, config.SessionStoreDir(), id)
	app := newHistoricalLifecycleApp(t)
	sourceID := historicalLifecycleID(t, app, id)
	if _, err := app.ImportHistoricalSession(sourceID); err != nil {
		t.Fatal(err)
	}
	binding, err := old.Open(t.Context(), session.SessionRef{HostID: "migration-source", SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Release(t.Context())
	defer old.Close(t.Context(), binding.Runtime().Ref())
	result := app.checkHistoricalSourceUpdate(t.Context(), sourceID, historicalSource{path: filepath.Join(config.SessionStoreDir(), id), format: "canonical", scope: "global"})
	if result.Status != "blocked" {
		t.Fatalf("source checked while writer owns runtime: %+v", result)
	}
}
