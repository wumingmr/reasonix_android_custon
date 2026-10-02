package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/session"
)

func requireTabPreset(t *testing.T, app *App, tab *WorkspaceTab, sessionID, want string) {
	t.Helper()
	app.mu.RLock()
	bound, got := tab.SessionID, tab.toolApprovalMode
	app.mu.RUnlock()
	if bound != sessionID {
		t.Fatalf("tab bound to %q, want %q", bound, sessionID)
	}
	if got != want {
		t.Fatalf("%s tab preset = %q, want %q", sessionID, got, want)
	}
	if ctrl := app.controllerForTab(tab); ctrl != nil {
		if live := normalizeToolApprovalMode(ctrl.ToolApprovalMode()); live != want {
			t.Fatalf("%s controller preset = %q, want %q", sessionID, live, want)
		}
	}
}

// presetHost pins a host that offers every preset, so these tests assert the
// same thing on a runner with or without an OS sandbox.
func presetHost(t *testing.T) {
	t.Helper()
	t.Cleanup(control.SetPresetSandboxForTest(true))
}

func choosePreset(t *testing.T, app *App, tab *WorkspaceTab, preset string) {
	t.Helper()
	seen, err := app.PermissionSnapshotForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SetPermissionPresetForTab(tab.ID, seen.SessionID, preset, seen.Revision); err != nil {
		t.Fatal(err)
	}
}

func restartDesktopFromTabsFile(t *testing.T, first *App, tab *WorkspaceTab) (*App, *WorkspaceTab) {
	t.Helper()
	if ctrl := first.controllerForTab(tab); ctrl != nil {
		ctrl.Close()
	}
	if tab.SharedHostKey != "" {
		first.releaseSharedHost(tab.SharedHostKey)
		tab.SharedHostKey = ""
	}
	first.closeSessionServices()
	app := NewApp()
	app.ctx = context.Background()
	installNoopRuntimeEvents(app)
	t.Cleanup(app.closeSessionServices)
	finishSavedTabMigration(app)
	app.tabsRestored = make(chan struct{})
	app.restoreOrBuildTabs()
	restored := app.tabs[tab.ID]
	if restored == nil {
		t.Fatalf("tab %q was not restored from %s", tab.ID, tabsFileName)
	}
	waitFor(t, "restored tab runtime", func() bool { return app.controllerForTab(restored) != nil })
	t.Cleanup(func() {
		if live := app.controllerForTab(restored); live != nil {
			live.Close()
		}
		if restored.SharedHostKey != "" {
			app.releaseSharedHost(restored.SharedHostKey)
		}
	})
	return app, restored
}

func TestCanonicalSessionPresetFollowsSessionAcrossNavigationAndRestart(t *testing.T) {
	presetHost(t)
	app, tab, target, rootB, _ := canonicalWorkspaceOpenFixture(t)
	refA := session.SessionRef{HostID: localDesktopHostID, SessionID: tab.SessionID}
	refB := target.Ref()
	_, fresh := desktopNewSessionDefaults("project", rootB)
	if fresh == control.ToolApprovalDangerFullAccess || fresh == control.ToolApprovalReadOnly {
		t.Fatalf("new-session default %q must differ from both recorded presets", fresh)
	}

	choosePreset(t, app, tab, control.ToolApprovalDangerFullAccess)
	requireTabPreset(t, app, tab, refA.SessionID, control.ToolApprovalDangerFullAccess)
	if _, err := app.OpenSession(refB); err != nil {
		t.Fatal(err)
	}
	requireTabPreset(t, app, tab, refB.SessionID, fresh)

	choosePreset(t, app, tab, control.ToolApprovalReadOnly)
	if _, err := app.OpenSession(refA); err != nil {
		t.Fatal(err)
	}
	requireTabPreset(t, app, tab, refA.SessionID, control.ToolApprovalDangerFullAccess)

	app, tab = restartDesktopFromTabsFile(t, app, tab)
	requireTabPreset(t, app, tab, refA.SessionID, control.ToolApprovalDangerFullAccess)
	if _, err := app.OpenSession(refB); err != nil {
		t.Fatal(err)
	}
	requireTabPreset(t, app, tab, refB.SessionID, control.ToolApprovalReadOnly)
	if _, err := app.OpenSession(refA); err != nil {
		t.Fatal(err)
	}
	requireTabPreset(t, app, tab, refA.SessionID, control.ToolApprovalDangerFullAccess)
}

func TestRestartDoesNotTrustSurfacePresetForUnrecordedSession(t *testing.T) {
	presetHost(t)
	app, tab, _, _, _ := canonicalWorkspaceOpenFixture(t)
	_, fresh := desktopNewSessionDefaults("project", tab.WorkspaceRoot)
	entry := persistedDesktopTabEntry(tab)
	entry.ToolApprovalMode = control.ToolApprovalDangerFullAccess
	file := desktopTabsFile{Tabs: []desktopTabEntry{entry}, ActiveTab: entry.ID, TabOrder: []string{entry.ID}}
	if err := os.MkdirAll(desktopConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.tabs, app.tabOrder = map[string]*WorkspaceTab{}, nil
	app.mu.Unlock()
	if err := os.WriteFile(filepath.Join(desktopConfigDir(), tabsFileName), mustMarshalJSON(t, file), 0o600); err != nil {
		t.Fatal(err)
	}

	app, tab = restartDesktopFromTabsFile(t, app, tab)
	requireTabPreset(t, app, tab, entry.SessionID, fresh)
}

func recordedSessionPresets(t *testing.T) map[string]string {
	t.Helper()
	body, err := os.ReadFile(sessionPresetsPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	var file sessionPresetsFile
	if err := json.Unmarshal(body, &file); err != nil {
		t.Fatal(err)
	}
	return file.Sessions
}

func TestNewSessionInTabDoesNotInheritSourcePreset(t *testing.T) {
	presetHost(t)
	app, tab, _, _, _ := canonicalWorkspaceOpenFixture(t)
	refA := session.SessionRef{HostID: localDesktopHostID, SessionID: tab.SessionID}
	_, fresh := desktopNewSessionDefaults("project", tab.WorkspaceRoot)
	if fresh == control.ToolApprovalDangerFullAccess {
		t.Fatalf("new-session default %q must differ from the source preset", fresh)
	}

	choosePreset(t, app, tab, control.ToolApprovalDangerFullAccess)
	if err := app.NewSessionForTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	app.mu.RLock()
	fresher := tab.SessionID
	app.mu.RUnlock()
	if fresher == "" || fresher == refA.SessionID {
		t.Fatalf("New Session did not rotate the tab: %q", fresher)
	}
	requireTabPreset(t, app, tab, fresher, fresh)
	app.saveTabsFromRemote()
	if preset, ok := recordedSessionPresets(t)[fresher]; ok {
		t.Fatalf("new session %s was recorded as %q without a choice made in it", fresher, preset)
	}

	app, tab = restartDesktopFromTabsFile(t, app, tab)
	requireTabPreset(t, app, tab, fresher, fresh)
	if _, err := app.OpenSession(refA); err != nil {
		t.Fatal(err)
	}
	requireTabPreset(t, app, tab, refA.SessionID, control.ToolApprovalDangerFullAccess)
	if _, err := app.OpenSession(session.SessionRef{HostID: localDesktopHostID, SessionID: fresher}); err != nil {
		t.Fatal(err)
	}
	requireTabPreset(t, app, tab, fresher, fresh)
}

func TestNewSessionOnBlankTabDropsItsEarlierChoice(t *testing.T) {
	presetHost(t)
	app, tab, _, _, _ := canonicalWorkspaceOpenFixture(t)
	_, fresh := desktopNewSessionDefaults("project", tab.WorkspaceRoot)
	if err := app.NewSessionForTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	app.mu.RLock()
	blank := tab.SessionID
	app.mu.RUnlock()
	choosePreset(t, app, tab, control.ToolApprovalDangerFullAccess)
	requireTabPreset(t, app, tab, blank, control.ToolApprovalDangerFullAccess)

	if err := app.NewSessionForTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	app.mu.RLock()
	reused := tab.SessionID
	app.mu.RUnlock()
	requireTabPreset(t, app, tab, reused, fresh)
	if preset, ok := recordedSessionPresets(t)[reused]; ok {
		t.Fatalf("session %s handed out as new still carries %q", reused, preset)
	}
}

func TestForkStartsAtNewSessionDefault(t *testing.T) {
	presetHost(t)
	app, tab, _, _, _ := canonicalWorkspaceOpenFixture(t)
	_, fresh := desktopNewSessionDefaults("project", tab.WorkspaceRoot)
	child, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: "session-fork", CWD: tab.WorkspaceRoot, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", tab.SessionWorkspace.ID, child.Ref().SessionID, ""); err != nil {
		t.Fatal(err)
	}

	choosePreset(t, app, tab, control.ToolApprovalDangerFullAccess)
	opened, err := app.openForkedSessionTabWithWorkspace(tab, forkedSessionLocator{SessionID: child.Ref().SessionID}, "")
	if err != nil {
		t.Fatal(err)
	}
	app.mu.RLock()
	forked := app.tabs[opened.tab.ID]
	app.mu.RUnlock()
	if forked == nil {
		t.Fatal("fork tab was not opened")
	}
	t.Cleanup(func() {
		if live := app.controllerForTab(forked); live != nil {
			live.Close()
		}
	})
	app.mu.RLock()
	got, mode := forked.toolApprovalMode, forked.mode
	app.mu.RUnlock()
	if got != fresh || mode != tabModeFromAxes(false, fresh == control.ToolApprovalDangerFullAccess) {
		t.Fatalf("fork preset = %q (mode %q), want %q", got, mode, fresh)
	}
	app.saveTabsFromRemote()
	if preset, ok := recordedSessionPresets(t)[child.Ref().SessionID]; ok {
		t.Fatalf("fork %s was recorded as %q without a choice made in it", child.Ref().SessionID, preset)
	}
}

func TestUnreadablePresetRecordsRestoreDefault(t *testing.T) {
	presetHost(t)
	app, tab, _, _, _ := canonicalWorkspaceOpenFixture(t)
	_, fresh := desktopNewSessionDefaults("project", tab.WorkspaceRoot)
	choosePreset(t, app, tab, control.ToolApprovalDangerFullAccess)
	if err := os.WriteFile(sessionPresetsPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, tab = restartDesktopFromTabsFile(t, app, tab)
	requireTabPreset(t, app, tab, tab.SessionID, fresh)
}

func TestStalePresetChoiceDoesNotLandOnNavigatedSession(t *testing.T) {
	presetHost(t)
	app, tab, target, rootB, _ := canonicalWorkspaceOpenFixture(t)
	refA := session.SessionRef{HostID: localDesktopHostID, SessionID: tab.SessionID}
	refB := target.Ref()
	_, fresh := desktopNewSessionDefaults("project", rootB)
	choosePreset(t, app, tab, control.ToolApprovalWorkspaceWrite)
	seen, err := app.PermissionSnapshotForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if seen.SessionID != refA.SessionID {
		t.Fatalf("snapshot session = %q, want %q", seen.SessionID, refA.SessionID)
	}
	if _, err := app.OpenSession(refB); err != nil {
		t.Fatal(err)
	}
	if live, err := app.PermissionSnapshotForTab(tab.ID); err != nil || live.Revision != seen.Revision {
		t.Fatalf("fixture must reproduce a revision collision: A=%d B=%d err=%v", seen.Revision, live.Revision, err)
	}

	_, err = app.SetPermissionPresetForTab(tab.ID, seen.SessionID, control.ToolApprovalDangerFullAccess, seen.Revision)
	if !errors.Is(err, errPermissionSessionChanged) {
		t.Fatalf("stale choice for %s on %s: err = %v, want errPermissionSessionChanged", refA.SessionID, refB.SessionID, err)
	}
	if !strings.Contains(err.Error(), "reasonix_error:"+permissionSessionChangedCode) {
		t.Fatalf("bridge error %q does not carry the refusal code", err)
	}
	if got, ok := recordedSessionPresets(t)[refB.SessionID]; ok {
		t.Fatalf("session %s recorded as %q from a choice made in %s", refB.SessionID, got, refA.SessionID)
	}
	requireTabPreset(t, app, tab, refB.SessionID, fresh)
}

func TestUnfencedModeSettersDoNotRecord(t *testing.T) {
	presetHost(t)
	app, tab, _, _, _ := canonicalWorkspaceOpenFixture(t)
	app.SetToolApprovalModeForTab(tab.ID, control.ToolApprovalDangerFullAccess)
	app.SetModeForTab(tab.ID, "yolo")
	if got, ok := recordedSessionPresets(t)[tab.SessionID]; ok {
		t.Fatalf("session %s recorded as %q by a setter that names no session", tab.SessionID, got)
	}
}

func TestPresetWriteFailureInReadOnlyDirFailsClosed(t *testing.T) {
	presetHost(t)
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not refuse writes here")
	}
	app, tab, _, _, _ := canonicalWorkspaceOpenFixture(t)
	_, fresh := desktopNewSessionDefaults("project", tab.WorkspaceRoot)
	choosePreset(t, app, tab, control.ToolApprovalDangerFullAccess)
	dir := filepath.Dir(sessionPresetsPath())
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	choosePreset(t, app, tab, control.ToolApprovalReadOnly)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	app, tab = restartDesktopFromTabsFile(t, app, tab)
	app.mu.RLock()
	got := tab.toolApprovalMode
	app.mu.RUnlock()
	if got == control.ToolApprovalDangerFullAccess {
		t.Fatalf("narrowed to read-only, write failed, restart restored %q (default %q)", got, fresh)
	}
}

func TestSnapshotBeforeBlankSessionReuseIsRefused(t *testing.T) {
	presetHost(t)
	app, tab, _, _, _ := canonicalWorkspaceOpenFixture(t)
	if err := app.NewSessionForTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	seen, err := app.PermissionSnapshotForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.NewSessionForTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	if live, err := app.PermissionSnapshotForTab(tab.ID); err != nil || live.SessionID != seen.SessionID {
		t.Fatalf("blank tab was not reused: before %q, after %q (err %v)", seen.SessionID, live.SessionID, err)
	}

	if _, err := app.SetPermissionPresetForTab(tab.ID, seen.SessionID, control.ToolApprovalDangerFullAccess, seen.Revision); err == nil {
		t.Errorf("choice read before New Session reused %s was accepted", seen.SessionID)
	}
	if got, ok := recordedSessionPresets(t)[seen.SessionID]; ok {
		t.Errorf("session %s handed out as new was recorded as %q from an earlier snapshot", seen.SessionID, got)
	}
}
