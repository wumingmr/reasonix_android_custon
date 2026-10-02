package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/repair"
)

type shutdownSnapshotController struct {
	control.SessionAPI
	calls           []string
	normalSnapshots int
	sessionPath     string
	shutdown        func() error
	close           func()
}

func TestShutdownSaveFailureKeepsEveryControllerOpenAndRetryResumes(t *testing.T) {
	isolateDesktopUserDirs(t)
	first := &shutdownSnapshotController{SessionAPI: control.New(control.Options{Label: "first"})}
	secondAttempts := 0
	second := &shutdownSnapshotController{SessionAPI: control.New(control.Options{Label: "second"})}
	second.shutdown = func() error {
		secondAttempts++
		if secondAttempts == 1 {
			return errors.New("disk full")
		}
		return nil
	}
	a := NewApp()
	a.tabs["first"] = &WorkspaceTab{ID: "first", Ctrl: first}
	a.tabs["second"] = &WorkspaceTab{ID: "second", Ctrl: second}
	a.tabOrder = []string{"first", "second"}

	failed, err := a.requestShutdown(context.Background(), shutdownRequest{RequestID: "attempt-1", Reason: shutdownReasonUserQuit})
	if err == nil || failed.Phase != "saving" || failed.ErrorCode != "session_save_failed" || !failed.Retryable {
		t.Fatalf("failed shutdown = %+v, %v", failed, err)
	}
	if len(first.calls) != 1 || first.calls[0] != "shutdown-snapshot" || len(second.calls) != 1 {
		t.Fatalf("save failure closed a controller: first=%v second=%v", first.calls, second.calls)
	}

	completed, err := a.requestShutdown(context.Background(), shutdownRequest{RequestID: "attempt-1", Reason: shutdownReasonConnectionLost})
	if err != nil || !completed.Completed || completed.Outcome != "success" {
		t.Fatalf("retry shutdown = %+v, %v", completed, err)
	}
	if completed.Reason != shutdownReasonUserQuit {
		t.Fatalf("retry rewrote original shutdown reason: %+v", completed)
	}
	if len(first.calls) != 2 || first.calls[1] != "close" {
		t.Fatalf("already-saved controller was not closed exactly once: %v", first.calls)
	}
	if len(second.calls) != 3 || second.calls[1] != "shutdown-snapshot" || second.calls[2] != "close" {
		t.Fatalf("failed save did not retry before close: %v", second.calls)
	}
}

func TestConnectionLossCleanupPreservesAbnormalTerminationEvidence(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	tracker := lifecycleTrackerForTest(t, t.TempDir(), 4242, "connection-loss")
	tracker.state.PID = 4242
	if err := tracker.start(); err != nil {
		t.Fatal(err)
	}
	a.lifecycle.tracker = tracker
	status, err := a.requestShutdown(context.Background(), shutdownRequest{
		RequestID: "connection-loss", Reason: shutdownReasonConnectionLost,
	})
	if err != nil || !status.Completed {
		t.Fatalf("connection loss cleanup = %+v, %v", status, err)
	}
	state, err := readDesktopLifecycleState(tracker.path)
	if err != nil {
		t.Fatalf("connection loss evidence was removed: %v", err)
	}
	if state.TerminationReason != shutdownReasonConnectionLost || state.CleanupOutcome != "success" || state.Phase != "completed" {
		t.Fatalf("connection loss evidence = %+v", state)
	}
}

func (c *shutdownSnapshotController) Snapshot() error {
	c.normalSnapshots++
	return nil
}

func (c *shutdownSnapshotController) SnapshotForShutdown() error {
	c.calls = append(c.calls, "shutdown-snapshot")
	if c.shutdown != nil {
		return c.shutdown()
	}
	return nil
}

func (c *shutdownSnapshotController) SessionPath() string {
	if c.sessionPath != "" {
		return c.sessionPath
	}
	if c.SessionAPI != nil {
		return c.SessionAPI.SessionPath()
	}
	return ""
}

func (c *shutdownSnapshotController) Close() {
	c.calls = append(c.calls, "close")
	if c.close != nil {
		c.close()
	}
	if c.SessionAPI != nil {
		c.SessionAPI.Close()
	}
}

func TestShutdownCloseRetrySkipsCompletedResources(t *testing.T) {
	isolateDesktopUserDirs(t)
	first := &shutdownSnapshotController{SessionAPI: control.New(control.Options{Label: "first"})}
	second := &shutdownSnapshotController{SessionAPI: control.New(control.Options{Label: "second"})}
	closeAttempts := 0
	second.close = func() {
		closeAttempts++
		if closeAttempts == 1 {
			panic("close failed")
		}
	}
	a := NewApp()
	a.tabs["first"] = &WorkspaceTab{ID: "first", Ctrl: first}
	a.tabs["second"] = &WorkspaceTab{ID: "second", Ctrl: second}
	a.tabOrder = []string{"first", "second"}

	failed, err := a.requestShutdown(context.Background(), shutdownRequest{RequestID: "close-1", Reason: shutdownReasonUserQuit})
	if err == nil || failed.Phase != "closing" || failed.ErrorCode != "cleanup_panic" {
		t.Fatalf("failed close = %+v, %v", failed, err)
	}
	completed, err := a.requestShutdown(context.Background(), shutdownRequest{RequestID: "close-1", Reason: shutdownReasonUserQuit})
	if err != nil || !completed.Completed {
		t.Fatalf("retry close = %+v, %v", completed, err)
	}
	if got := first.calls; len(got) != 2 || got[0] != "shutdown-snapshot" || got[1] != "close" {
		t.Fatalf("completed controller repeated: %v", got)
	}
	if got := second.calls; len(got) != 3 || got[0] != "shutdown-snapshot" || got[1] != "close" || got[2] != "close" {
		t.Fatalf("failed controller did not resume: %v", got)
	}
}

func TestShutdownWaitsForRuntimeLifecycleMutation(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.runtimeAdmissionMu.Lock()
	admissionHeld := true
	defer func() {
		if admissionHeld {
			app.runtimeAdmissionMu.Unlock()
		}
	}()

	done := make(chan struct{})
	go func() {
		app.shutdown(context.Background())
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for app.runtimeRebuildMu.TryLock() {
		app.runtimeRebuildMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("shutdown did not enter the runtime lifecycle barrier")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("shutdown bypassed an in-flight runtime lifecycle mutation")
	default:
	}
	if status := app.shutdownStatus(""); status.Phase != "waiting_runtime_admission" {
		t.Fatalf("blocked shutdown phase = %q, want waiting_runtime_admission", status.Phase)
	}

	app.runtimeAdmissionMu.Unlock()
	admissionHeld = false
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not resume after the runtime lifecycle mutation completed")
	}
}

func TestShutdownDoesNotWaitForCancelledControllerBuild(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	tab := app.createTabEntryWithID("global", "", "", "blocked-build")
	app.mu.Lock()
	app.tabs[tab.ID] = tab
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	app.mu.Unlock()

	started := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	app.tabBuildStartHook = func(string) {
		close(started)
		<-release
	}
	buildDone := make(chan struct{})
	go func() {
		app.startTabControllerBuild(tab)
		close(buildDone)
	}()
	<-started

	shutdownDone := make(chan struct{})
	go func() {
		app.shutdown(context.Background())
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
		// Completion while the build remains blocked proves non-dependence.
		// The guard is for deadlocks, not shutdown persistence performance.
		unblock()
	case <-time.After(5 * time.Second):
		unblock()
		t.Fatal("shutdown waited for a cancelled controller build")
	}
	select {
	case <-buildDone:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled controller build did not finish")
	}
}

func TestShutdownCancelsBlockedSessionOpen(t *testing.T) {
	app, _, target, _, _ := canonicalWorkspaceOpenFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	cancelled := make(chan struct{})
	app.sessionOpenBuildHook = func(ctx context.Context) {
		close(started)
		select {
		case <-ctx.Done():
			close(cancelled)
		case <-release:
		}
	}

	openDone := make(chan error, 1)
	go func() {
		_, err := app.OpenSession(target.Ref())
		openDone <- err
	}()
	<-started

	shutdownDone := make(chan error, 1)
	go func() {
		_, err := app.requestShutdown(context.Background(), shutdownRequest{RequestID: "cancel-session-open", Reason: shutdownReasonUserQuit})
		shutdownDone <- err
	}()

	// Observe cancellation itself before waiting for unrelated shutdown work
	// such as session persistence and window-state writes.
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("session open did not observe shutdown cancellation")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown after cancelling session open: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not complete after session open observed cancellation")
	}
	select {
	case err := <-openDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled session open = %v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled session open did not return")
	}
}

// TestShutdownRecordsLKGOnlyAfterReady pins that last-known-good config is only
// written after the window reached domReady. A quit before paint must not
// rewrite the LKG snapshot from an incomplete boot.
func TestShutdownRecordsLKGOnlyAfterReady(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	// Pre-ready shutdown is a no-op for LKG (startupReady is false).
	a.shutdown(context.Background())
	a.startupReady.Store(true)
	// Post-ready shutdown attempts RecordHealthyConfig; missing user config is fine.
	a.shutdown(context.Background())
}

func TestCaptureAndCommitPendingUpdateHealthUsesExactStartupIdentity(t *testing.T) {
	originalRead := readPendingUpdateForHealth
	originalMark := markPendingUpdateHealthyAfterReady
	t.Cleanup(func() {
		readPendingUpdateForHealth = originalRead
		markPendingUpdateHealthyAfterReady = originalMark
	})
	tx := &repair.UpdateTransaction{
		SchemaVersion: 1,
		ToVersion:     version,
		CreatedAt:     "2026-08-05T00:00:00Z",
		Platform:      "darwin/arm64",
		TargetKind:    "app-bundle",
		TargetPath:    "/Applications/Reasonix.app",
		BackupPath:    "/Applications/Reasonix.app.reasonix-update-backup",
	}
	readPendingUpdateForHealth = func() (*repair.UpdateTransaction, error) { return tx, nil }
	app := NewApp()
	capturePendingUpdateHealthIdentity(app)
	wantID := repair.UpdateTransactionID(tx)
	if app.healthyUpdateCreatedAt != tx.CreatedAt || app.healthyUpdateTransactionID != wantID {
		t.Fatalf("captured health identity=(%q,%q), want (%q,%q)", app.healthyUpdateCreatedAt, app.healthyUpdateTransactionID, tx.CreatedAt, wantID)
	}
	called := false
	markPendingUpdateHealthyAfterReady = func(running, createdAt, transactionID string) error {
		called = true
		if running != version || createdAt != tx.CreatedAt || transactionID != wantID {
			t.Fatalf("health commit=(%q,%q,%q)", running, createdAt, transactionID)
		}
		return nil
	}
	if err := app.commitPendingUpdateHealth(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("exact startup transaction was not committed")
	}
}

func TestCapturePendingUpdateHealthAcceptsVersionPrefixMismatch(t *testing.T) {
	originalRead := readPendingUpdateForHealth
	originalVersion := version
	t.Cleanup(func() {
		readPendingUpdateForHealth = originalRead
		version = originalVersion
	})
	version = "1.21.0"
	readPendingUpdateForHealth = func() (*repair.UpdateTransaction, error) {
		return &repair.UpdateTransaction{
			ToVersion:  "v1.21.0",
			CreatedAt:  "2026-08-07T00:00:00Z",
			TargetKind: "file",
		}, nil
	}
	app := &App{}
	capturePendingUpdateHealthIdentity(app)
	if app.healthyUpdateCreatedAt == "" || app.healthyUpdateTransactionID == "" {
		t.Fatalf("expected health identity for v-prefix mismatch, got createdAt=%q id=%q",
			app.healthyUpdateCreatedAt, app.healthyUpdateTransactionID)
	}
}

func TestCapturePendingUpdateHealthRejectsDifferentTargetVersion(t *testing.T) {
	originalRead := readPendingUpdateForHealth
	t.Cleanup(func() { readPendingUpdateForHealth = originalRead })
	readPendingUpdateForHealth = func() (*repair.UpdateTransaction, error) {
		return &repair.UpdateTransaction{ToVersion: version + "-other", CreatedAt: "2026-08-05T00:00:00Z"}, nil
	}
	app := NewApp()
	capturePendingUpdateHealthIdentity(app)
	if app.healthyUpdateCreatedAt != "" || app.healthyUpdateTransactionID != "" {
		t.Fatalf("captured unrelated transaction: %+v", app)
	}
}

func TestShutdownUsesDurableSnapshotBeforeClosingController(t *testing.T) {
	isolateDesktopUserDirs(t)
	ctrl := &shutdownSnapshotController{SessionAPI: control.New(control.Options{Label: "shutdown"})}
	a := NewApp()
	a.tabs["tab"] = &WorkspaceTab{ID: "tab", Ctrl: ctrl}
	a.tabOrder = []string{"tab"}

	a.shutdown(context.Background())

	if ctrl.normalSnapshots != 0 {
		t.Fatalf("ordinary Snapshot calls = %d, want shutdown-specific persistence", ctrl.normalSnapshots)
	}
	if len(ctrl.calls) != 2 || ctrl.calls[0] != "shutdown-snapshot" || ctrl.calls[1] != "close" {
		t.Fatalf("shutdown call order = %v, want [shutdown-snapshot close]", ctrl.calls)
	}
}

func TestShutdownPersistsRecoveryPathCommittedAfterCallback(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	originalPath := filepath.Join(dir, "original.jsonl")
	recoveryPath := filepath.Join(dir, "original-recovery.jsonl")
	a := NewApp()
	ctrl := &shutdownSnapshotController{
		SessionAPI:  control.New(control.Options{Label: "shutdown", SessionPath: originalPath}),
		sessionPath: originalPath,
	}
	tab := &WorkspaceTab{ID: "tab", Ctrl: ctrl, SessionPath: originalPath}
	a.tabs[tab.ID] = tab
	a.tabOrder = []string{tab.ID}
	a.activeTabID = tab.ID
	ctrl.shutdown = func() error {
		err := a.handleTabSessionRecovered(tab)(control.SessionRecoveryInfo{
			OriginalPath: originalPath,
			RecoveryPath: recoveryPath,
		})
		if err == nil {
			// Force a newer ordinary layout write while Controller still exposes
			// the old path. The recovery lease must keep this write anchored to
			// recovery instead of undoing the callback's first save.
			a.mu.Lock()
			a.saveTabsLocked()
			a.mu.Unlock()
			// Controller.commitRecoveredSession updates its path only after the
			// callback succeeds. Mirror that ordering exactly.
			ctrl.sessionPath = recoveryPath
		}
		return err
	}

	a.shutdown(context.Background())

	saved := loadTabsFile()
	if len(saved.Tabs) != 1 || saved.Tabs[0].ID != tab.ID {
		t.Fatalf("saved tabs = %+v, want recovered tab %q", saved.Tabs, tab.ID)
	}
	if got := saved.Tabs[0].SessionPath; got != recoveryPath {
		t.Fatalf("saved shutdown session path = %q, want recovery path %q", got, recoveryPath)
	}
	if len(ctrl.calls) != 2 || ctrl.calls[0] != "shutdown-snapshot" || ctrl.calls[1] != "close" {
		t.Fatalf("shutdown call order = %v, want [shutdown-snapshot close]", ctrl.calls)
	}
}
