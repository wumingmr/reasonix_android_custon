package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"reasonix/internal/identitylock"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func newHistoricalLifecycleApp(t *testing.T) *App {
	t.Helper()
	app := NewApp()
	app.ctx = t.Context()
	installNoopRuntimeEvents(app)
	t.Cleanup(app.closeSessionServices)
	t.Cleanup(app.stopHistoricalImports)
	return app
}

func historicalLifecycleID(t *testing.T, app *App, title string) string {
	t.Helper()
	list, err := app.ListHistoricalSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range list.Items {
		if view.Title == title {
			return view.ID
		}
	}
	t.Fatalf("historical source %q missing: %+v", title, list)
	return ""
}

func awaitHistoricalBatch(t *testing.T, app *App) HistoricalImportStatus {
	t.Helper()
	// Start/Resume admits its worker before returning. Join that owner instead
	// of launching directory discovery to poll it; discovery is intentionally
	// rejected after shutdown and is not evidence that a batch has drained.
	app.historicalImports.workers.Wait()
	status := app.GetHistoricalImportStatus()
	if status.Running {
		t.Fatalf("historical worker drained while batch remained running: %+v", status)
	}
	return status
}

func TestHistoricalBatchContinuesPastBusySource(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	coldV4MigrationFixture(t, root, "busy")
	coldV4MigrationFixture(t, root, "available")
	app := newHistoricalLifecycleApp(t)
	busy := historicalLifecycleID(t, app, "busy")
	available := historicalLifecycleID(t, app, "available")
	release, err := identitylock.Acquire(t.Context(), filepath.Join(root, ".busy.ownership.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := app.StartHistoricalImport([]string{busy, available}); err != nil {
		t.Fatal(err)
	}
	status := awaitHistoricalBatch(t, app)
	states := map[string]string{}
	for _, view := range status.Items {
		states[view.ID] = view.Status
	}
	if states[busy] != "blocked" || states[available] != "imported" {
		t.Fatalf("one occupied source prevented independent progress: %+v", status)
	}
	if !app.runtimeRebuildMu.TryLock() {
		t.Fatal("batch retained global runtime gate")
	}
	app.runtimeRebuildMu.Unlock()
}

func TestHistoricalConcurrentRequestsKeepOneTarget(t *testing.T) {
	isolateDesktopUserDirs(t)
	coldV4MigrationFixture(t, config.SessionStoreDir(), "same-source")
	app := newHistoricalLifecycleApp(t)
	id := historicalLifecycleID(t, app, "same-source")
	entered, proceed := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(proceed) }) }
	t.Cleanup(release)
	var commits atomic.Int32
	app.desktopSessions.beforeMigrationRegistryCommit = func() error {
		if commits.Add(1) == 1 {
			close(entered)
			<-proceed
		}
		return nil
	}
	type outcome struct {
		result SessionRestoreResult
		err    error
	}
	results := make(chan outcome, 2)
	go func() { result, err := app.ImportHistoricalSession(id); results <- outcome{result, err} }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first import did not reach publication")
	}
	started := make(chan struct{})
	go func() {
		close(started)
		result, err := app.ImportHistoricalSession(id)
		results <- outcome{result, err}
	}()
	<-started
	release()
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.result.Session != second.result.Session {
		t.Fatalf("duplicate import diverged: %+v / %+v", first, second)
	}
	if commits.Load() != 1 {
		t.Fatalf("duplicate request published %d times", commits.Load())
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil || len(state.SourceMappings) != 1 || len(state.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs) != 1 {
		t.Fatalf("duplicate durable identities: %+v %v", state, err)
	}
}

func TestPrepareSessionReturnsRevisionedSharedTask(t *testing.T) {
	isolateDesktopUserDirs(t)
	coldV4MigrationFixture(t, config.SessionStoreDir(), "prepared-navigation")
	app := newHistoricalLifecycleApp(t)
	list, err := app.ListHistoricalSessions()
	if err != nil || len(list.Items) != 1 || list.Items[0].Source == nil {
		t.Fatalf("historical listing: %+v %v", list, err)
	}
	entered, proceed := make(chan struct{}), make(chan struct{})
	app.desktopSessions.beforeMigrationRegistryCommit = func() error {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-proceed
		return nil
	}
	first, err := app.PrepareSession(SessionSelector{Source: list.Items[0].Source})
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.PrepareSession(SessionSelector{Source: list.Items[0].Source})
	if err != nil || first.OperationID != second.OperationID {
		t.Fatalf("duplicate prepare did not join: %+v %+v %v", first, second, err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("preparation did not reach publication")
	}
	close(proceed)
	deadline := time.Now().Add(10 * time.Second)
	for {
		view, getErr := app.GetSessionPreparation(first.OperationID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if view.Status == "ready" {
			if view.Target == nil || view.Revision <= first.Revision {
				t.Fatalf("invalid terminal preparation: %+v", view)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("preparation did not complete: %+v", view)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHistoricalQueueRestartsPaused(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	coldV4MigrationFixture(t, root, "queue-one")
	coldV4MigrationFixture(t, root, "queue-two")
	app := newHistoricalLifecycleApp(t)
	first := historicalLifecycleID(t, app, "queue-one")
	second := historicalLifecycleID(t, app, "queue-two")
	c := &app.historicalImports
	c.mu.Lock()
	c.queue = []string{first, second}
	if err := c.saveQueueLocked(); err != nil {
		c.mu.Unlock()
		t.Fatal(err)
	}
	c.mu.Unlock()
	app.closeSessionServices()
	app = newHistoricalLifecycleApp(t)
	status, err := app.ListHistoricalSessions()
	if err != nil {
		t.Fatal(err)
	}
	if status.Running || !status.Paused || status.Remaining != 2 {
		t.Fatalf("restarted queue must wait for manual continue: %+v", status)
	}
}

func TestHistoricalSourceUpdateImportsOneStableBranch(t *testing.T) {
	for _, oldIdentity := range []bool{false, true} {
		name := "current"
		if oldIdentity {
			name = "old-identity"
		}
		t.Run(name, func(t *testing.T) { testHistoricalSourceUpdateImportsOneStableBranch(t, oldIdentity) })
	}
}

func testHistoricalSourceUpdateImportsOneStableBranch(t *testing.T, oldIdentity bool) {
	isolateDesktopUserDirs(t)
	old := coldV4MigrationFixture(t, config.SessionStoreDir(), "updated-source")
	app := newHistoricalLifecycleApp(t)
	list, err := app.ListHistoricalSessions()
	if err != nil || len(list.Items) != 1 || list.Items[0].Source == nil {
		t.Fatalf("list: %+v %v", list, err)
	}
	base, err := app.ImportHistoricalSession(list.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldIdentity {
		rewriteHistoricalSourceIdentityForTest(t, app, list.Items[0].ID)
	}
	binding, err := old.Open(t.Context(), session.SessionRef{HostID: "migration-source", SessionID: "updated-source"})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "new", Role: provider.RoleAssistant, Content: "new historical content"}})
	if _, err := binding.Runtime().Session().AppendBatch(t.Context(), "new-content", []session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	oldRef := binding.Runtime().Ref()
	if err := binding.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(t.Context(), oldRef); err != nil {
		t.Fatal(err)
	}
	selector := SessionSelector{Ref: &base.Session}
	update, err := app.CheckHistoricalSourceUpdate(selector)
	if err != nil || update.Status != "checking" {
		t.Fatalf("initial check: %+v %v", update, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for update.Status == "checking" {
		if time.Now().After(deadline) {
			t.Fatal("source update check did not complete")
		}
		time.Sleep(time.Millisecond)
		update, err = app.CheckHistoricalSourceUpdate(selector)
		if err != nil {
			t.Fatal(err)
		}
	}
	if update.Status != "available" || update.Version == "" || update.Source == nil {
		t.Fatalf("updated source not detected: %+v", update)
	}
	prepared, err := app.PrepareHistoricalSourceVersion(*update.Source, update.Version)
	if err != nil {
		t.Fatal(err)
	}
	for prepared.Status != "ready" {
		if time.Now().After(deadline) {
			t.Fatal("updated source preparation did not complete")
		}
		time.Sleep(time.Millisecond)
		prepared, err = app.GetSessionPreparation(prepared.OperationID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if prepared.Target == nil || prepared.Target.SessionID == base.Session.SessionID {
		t.Fatalf("source update did not create an independent branch: base=%+v update=%+v", base, prepared)
	}
	again, err := app.PrepareHistoricalSourceVersion(*list.Items[0].Source, update.Version)
	if err != nil || again.OperationID != prepared.OperationID {
		t.Fatalf("same version was not deduplicated: %+v %v", again, err)
	}
}

func TestHistoricalCancelCanRestartDurableImport(t *testing.T) {
	isolateDesktopUserDirs(t)
	coldV4MigrationFixture(t, config.SessionStoreDir(), "cancelled")
	app := newHistoricalLifecycleApp(t)
	id := historicalLifecycleID(t, app, "cancelled")
	entered, proceed := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(proceed) }) }
	t.Cleanup(release)
	var hooks atomic.Int32
	app.desktopSessions.beforeMigrationRegistryCommit = func() error {
		if hooks.Add(1) == 1 {
			close(entered)
			<-proceed
		}
		return nil
	}
	if _, err := app.StartHistoricalImport([]string{id}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("import did not reach publication")
	}
	if _, err := app.ControlHistoricalImport("cancel"); err != nil {
		t.Fatal(err)
	}
	release()
	awaitHistoricalBatch(t, app)
	if _, err := app.StartHistoricalImport([]string{id}); err != nil {
		t.Fatalf("cancel permanently disabled explicit import: %v", err)
	}
	status := awaitHistoricalBatch(t, app)
	if len(status.Items) != 1 || status.Items[0].Status != "imported" {
		t.Fatalf("cancelled durable import was not resumed: %+v", status)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil || len(state.SourceMappings) != 1 || len(state.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs) != 1 {
		t.Fatalf("restart duplicated target: %+v %v", state, err)
	}
}

func TestHistoricalLateCancelCannotCancelRetry(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := config.SessionStoreDir()
	coldV4MigrationFixture(t, root, "late-cancel")
	app := newHistoricalLifecycleApp(t)
	id := historicalLifecycleID(t, app, "late-cancel")
	releaseSource, err := identitylock.Acquire(t.Context(), filepath.Join(root, ".late-cancel.ownership.lock"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := app.prepareHistoricalSession(id, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = waitHistoricalImport(first); err == nil {
		t.Fatal("occupied source did not block the first preparation")
	}
	releaseSource()

	entered, proceed := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(proceed) }) }
	t.Cleanup(release)
	app.desktopSessions.beforeMigrationRegistryCommit = func() error {
		close(entered)
		<-proceed
		return nil
	}
	second, err := app.prepareHistoricalSession(id, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.operationID == second.operationID {
		t.Fatalf("retry reused operation id %q", second.operationID)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("retry did not reach publication")
	}
	if _, err := app.CancelSessionPreparation(first.operationID); err == nil {
		t.Fatal("stale operation id remained cancellable after retry")
	}
	if err := second.ctx.Err(); err != nil {
		t.Fatalf("stale cancellation reached retry: %v", err)
	}
	release()
	if result, err := waitHistoricalImport(second); err != nil || result.Session.SessionID == "" {
		t.Fatalf("retry did not complete after stale cancellation: %+v %v", result, err)
	}
}

func TestHistoricalCancelledOperationRetryGetsNewIdentity(t *testing.T) {
	isolateDesktopUserDirs(t)
	coldV4MigrationFixture(t, config.SessionStoreDir(), "cancel-retry")
	app := newHistoricalLifecycleApp(t)
	id := historicalLifecycleID(t, app, "cancel-retry")
	entered, proceed := make(chan struct{}), make(chan struct{})
	var hookCalls atomic.Int32
	app.desktopSessions.beforeMigrationRegistryCommit = func() error {
		if hookCalls.Add(1) == 1 {
			close(entered)
			<-proceed
		}
		return nil
	}
	first, err := app.prepareHistoricalSession(id, true, false)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first preparation did not reach the cancellation point")
	}
	if _, err = app.CancelSessionPreparation(first.operationID); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	if _, err = waitHistoricalImport(first); err == nil {
		t.Fatal("cancelled preparation completed successfully")
	}

	second, err := app.prepareHistoricalSession(id, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.operationID == first.operationID {
		t.Fatalf("retry reused cancelled operation id %q", second.operationID)
	}
	result, err := waitHistoricalImport(second)
	if err != nil || result.Session.SessionID == "" {
		t.Fatalf("retry did not complete: %+v %v", result, err)
	}
	if _, err = app.GetSessionPreparation(first.operationID); err == nil {
		t.Fatal("cancelled operation remained addressable after retry")
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil || len(state.SourceMappings) != 1 || len(state.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs) != 1 {
		t.Fatalf("cancel retry duplicated the durable target: %+v %v", state, err)
	}
}

func TestHistoricalInteractiveCancelPreservesBatchDemand(t *testing.T) {
	isolateDesktopUserDirs(t)
	coldV4MigrationFixture(t, config.SessionStoreDir(), "shared-demand")
	app := newHistoricalLifecycleApp(t)
	id := historicalLifecycleID(t, app, "shared-demand")
	entered, proceed := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(proceed) }) }
	t.Cleanup(release)
	app.desktopSessions.beforeMigrationRegistryCommit = func() error {
		close(entered)
		<-proceed
		return nil
	}
	interactive, err := app.prepareHistoricalSession(id, true, false)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := app.prepareHistoricalSession(id, false, true)
	if err != nil || batch != interactive {
		t.Fatalf("interactive and batch demands did not join: %p %p %v", interactive, batch, err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("shared preparation did not reach publication")
	}
	if _, err = app.CancelSessionPreparation(interactive.operationID); err != nil {
		t.Fatal(err)
	}
	if err = interactive.ctx.Err(); err != nil {
		t.Fatalf("interactive cancellation stopped the batch demand: %v", err)
	}
	release()
	if result, err := waitHistoricalImport(interactive); err != nil || result.Session.SessionID == "" {
		t.Fatalf("batch demand did not finish: %+v %v", result, err)
	}
}

func TestHistoricalImportDoesNotReviveArchivedOrDeletedTarget(t *testing.T) {
	for _, lifecycle := range []string{workspacestate.Archived, workspacestate.Deleted} {
		t.Run(lifecycle, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			root := config.SessionStoreDir()
			coldV4MigrationFixture(t, root, "retained-source")
			original := startupHistorySourceBytes(t, root, "retained-source")
			app := newHistoricalLifecycleApp(t)
			id := historicalLifecycleID(t, app, "retained-source")
			result, err := app.ImportHistoricalSession(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := app.ArchiveCanonicalSession(result.Session); err != nil {
				t.Fatal(err)
			}
			if lifecycle == workspacestate.Deleted {
				if err := app.PurgeCanonicalSession(result.Session); err != nil {
					t.Fatal(err)
				}
			}
			retired, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			retainedMemberships := len(retired.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs)
			app.closeSessionServices()
			app = newHistoricalLifecycleApp(t)
			if _, err := app.ImportHistoricalSession(id); err == nil {
				t.Fatal("explicit import silently revived a retired target")
			}
			if _, err := app.StartHistoricalImport(nil); err != nil {
				t.Fatal(err)
			}
			awaitHistoricalBatch(t, app)
			state, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if state.SessionStates[result.Session.SessionID].Lifecycle != lifecycle || len(state.Workspaces[workspacestate.GlobalWorkspaceID].SessionIDs) != retainedMemberships || len(state.SourceMappings) != 1 {
				t.Fatalf("rescan/import revived target: %+v", state)
			}
			if lifecycle == workspacestate.Archived {
				assertStartupHistorySourceUnchanged(t, root, "retained-source", original)
			} else if _, err := os.Stat(filepath.Join(root, "retained-source")); !os.IsNotExist(err) {
				t.Fatalf("exclusive original survived purge: %v", err)
			}
		})
	}
}

func TestHistoricalImportResumesPriorDurablePhase(t *testing.T) {
	for _, scenario := range []string{"prepared", "content_ready", "content_ready_old_metadata", "prepared_old_identity", "content_ready_old_identity"} {
		t.Run(scenario, func(t *testing.T) {
			phase := strings.TrimSuffix(scenario, "_old_identity")
			isolateDesktopUserDirs(t)
			root := config.SessionStoreDir()
			const sessionID = "interrupted-source"
			old := coldV4MigrationFixture(t, root, sessionID)
			app := newHistoricalLifecycleApp(t)
			workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, sessionID)
			fingerprint, err := desktopSourceFingerprint(path)
			if err != nil {
				t.Fatal(err)
			}
			opID, err := app.prepareDesktopImport(t.Context(), desktopMigrationSource{scope: "global"}, path, fingerprint, sessionID, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if phase != "prepared" {
				bundle := filepath.Join(t.TempDir(), "bundle")
				if err := old.Export(t.Context(), session.SessionRef{HostID: "migration-source", SessionID: sessionID}, bundle); err != nil {
					t.Fatal(err)
				}
				if _, err := app.desktopSessionService("").ImportWithHeader(t.Context(), bundle, session.CreateOptions{SessionID: sessionID, CWD: globalWorkspaceRoot(), Origin: session.SessionOriginCanonicalImport}); err != nil {
					t.Fatal(err)
				}
				if phase == "content_ready_old_metadata" {
					// Existing durable operations can omit optional presentation and
					// retained-artifact metadata; recovery must honor that snapshot.
					state, err := app.workspaceRegistry().Load(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					if err := app.workspaceRegistry().PrepareOperationContent(t.Context(), opID, []string{sessionID}, state.PendingOperations[opID].Mapping, nil); err != nil {
						t.Fatal(err)
					}
				} else {
					// Stop the real publication path immediately before CommitOperation,
					// retaining its complete provenance and presentation snapshot.
					source := desktopMigrationSource{scope: "global", operationID: opID, deferArchive: true}
					if err := app.commitDesktopImport(t.Context(), source, path, "canonical", fingerprint, sessionID, workspace); err != nil {
						t.Fatal(err)
					}
				}
			}
			if strings.HasSuffix(scenario, "_old_identity") {
				rewriteHistoricalSourceIdentityForTest(t, app, desktopSourceKey(path, ""))
			}
			before, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			app.closeSessionServices()
			app = newHistoricalLifecycleApp(t)
			id := historicalLifecycleID(t, app, sessionID)
			result, err := app.ImportHistoricalSession(id)
			if err != nil || result.Session.SessionID != sessionID {
				t.Fatalf("durable %s import changed identity or failed: %+v %v", phase, result, err)
			}
			state, err := app.workspaceRegistry().Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if state.PendingOperations[opID].Phase != "committed" || len(state.SourceMappings) != 1 {
				t.Fatalf("prior operation was not committed exactly once: %+v", state.PendingOperations)
			}
			if phase != "prepared" {
				previous, committed := before.PendingOperations[opID], state.PendingOperations[opID]
				if !reflect.DeepEqual(previous.Mapping, committed.Mapping) || !reflect.DeepEqual(previous.Presentation, committed.Presentation) {
					t.Fatal("resuming content_ready rewrote its durable metadata")
				}
			}
			if _, err := app.desktopSessionService("").Query().Snapshot(t.Context(), result.Session); err != nil {
				t.Fatal(err)
			}
		})
	}
}
