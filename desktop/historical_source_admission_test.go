package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func restoreHistoricalSourcePendingTab(t *testing.T) (*App, *WorkspaceTab, string) {
	t.Helper()
	app := newSavedTabReconcileTestApp(t)
	t.Cleanup(app.closeSessionServices)
	path := filepath.Join(config.SessionDir(), "pending.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := agent.NewSession("system")
	legacy.Add(provider.Message{ID: "old", Role: provider.RoleUser, Content: "original work"})
	if err := legacy.Save(path); err != nil {
		t.Fatal(err)
	}
	entry := desktopTabEntry{ID: "pending", Scope: "global", TopicID: "topic", SessionPath: path}
	file := desktopTabsFile{Tabs: []desktopTabEntry{entry}, ActiveTab: entry.ID, TabOrder: []string{entry.ID}}
	if err := os.MkdirAll(desktopConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(desktopConfigDir(), tabsFileName), mustMarshalJSON(t, file), 0o600); err != nil {
		t.Fatal(err)
	}
	finishSavedTabMigration(app)
	app.tabsRestored = make(chan struct{})
	app.restoreOrBuildTabs()
	tab := waitForTabReady(t, app, entry.ID)
	native, ok := tab.Ctrl.(*control.Controller)
	if !ok || !native.NativeLegacySession() || agent.CanonicalSessionPath(tab.SessionPath) != agent.CanonicalSessionPath(path) {
		t.Fatalf("pending source did not restore as its native runtime: path=%q ctrl=%T", tab.SessionPath, tab.Ctrl)
	}
	return app, tab, path
}

func TestHistoricalSourcePendingTabRefusesSendWithTypedReason(t *testing.T) {
	app, tab, path := restoreHistoricalSourcePendingTab(t)

	err := app.SubmitToTabWithID(tab.ID, "continue", "submission-1")
	var refusal *SessionOperationError
	if !errors.As(err, &refusal) || refusal.Code != sessionOperationHistoricalSourcePending {
		t.Fatalf("send was not refused with the historical-source reason: %v", err)
	}
	if !errors.Is(err, control.ErrSubmissionNotAccepted) {
		t.Fatalf("refusal does not say the submission was not accepted: %v", err)
	}
	if errors.Is(err, control.ErrSubmissionIdentityUnavailable) {
		t.Fatalf("send reached the controller before being refused: %v", err)
	}
	if tab.Ctrl.RuntimeStatus().Running {
		t.Fatal("refused send started a turn")
	}

	meta := app.metaForTab(tab.ID)
	if meta.HistoricalSource == nil || agent.CanonicalSessionPath(meta.HistoricalSource.Path) != agent.CanonicalSessionPath(path) {
		t.Fatalf("meta does not report the pending source, so the composer stays enabled: %+v", meta.HistoricalSource)
	}
	for _, listed := range app.ListTabs() {
		if listed.ID == tab.ID && listed.HistoricalSource == nil {
			t.Fatal("tab list does not report the pending source")
		}
	}
}

func TestHistoricalSourcePendingTabPreparesIntoCanonicalSession(t *testing.T) {
	app, tab, _ := restoreHistoricalSourcePendingTab(t)
	source := app.metaForTab(tab.ID).HistoricalSource
	if source == nil {
		t.Fatal("meta does not report the pending source")
	}
	view, err := app.PrepareSession(SessionSelector{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for view.Status != "ready" && view.Status != "failed" && view.Status != "blocked" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		if view, err = app.GetSessionPreparation(view.OperationID); err != nil {
			t.Fatal(err)
		}
	}
	if view.Status != "ready" || view.Target == nil {
		t.Fatalf("pending source could not be prepared while its tab is open: %+v", view)
	}
}

func TestNativeLegacyRuntimeRefusesIdentifiedSendBeforeController(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "native.jsonl")
	ctrl := control.New(control.Options{Sink: &tabEventSink{tabID: "tab"}, SessionDir: dir, SessionPath: path, NativeLegacySession: true})
	defer ctrl.Close()
	tab := &WorkspaceTab{ID: "tab", Scope: "global", Ready: true, Ctrl: ctrl, sink: &tabEventSink{tabID: "tab"}, SessionPath: path}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}, activeTabID: tab.ID}

	err := app.SubmitToTabWithID(tab.ID, "continue", "submission-1")
	var refusal *SessionOperationError
	if !errors.As(err, &refusal) || refusal.Code != sessionOperationHistoricalSourcePending {
		t.Fatalf("send was not refused with the historical-source reason: %v", err)
	}
	if errors.Is(err, control.ErrSubmissionIdentityUnavailable) {
		t.Fatalf("send reached the controller before being refused: %v", err)
	}
}

// Importing a previewed legacy source and moving to the imported session keeps
// the source byte-for-byte, so the source is not reported as updated.
func TestHistoricalPreviewImportLeavesSourceUntouched(t *testing.T) {
	app, tab, path := restoreHistoricalSourcePendingTab(t)
	before, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	view, err := app.PrepareSession(SessionSelector{Source: app.metaForTab(tab.ID).HistoricalSource})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); view.Status != "ready" && view.Status != "failed"; {
		if time.Now().After(deadline) {
			t.Fatalf("preparation did not settle: %+v", view)
		}
		time.Sleep(20 * time.Millisecond)
		view, _ = app.GetSessionPreparation(view.OperationID)
	}
	if view.Status != "ready" {
		t.Fatalf("preparation = %+v", view)
	}
	if _, err := app.OpenSession(session.SessionRef{HostID: view.Target.HostID, SessionID: view.Target.SessionID}); err != nil {
		t.Fatal(err)
	}
	if after, err := desktopSourceFingerprint(path); err != nil || after != before {
		t.Fatalf("opening the imported session rewrote the legacy source (err=%v)", err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		update, err := app.CheckHistoricalSourceUpdate(SessionSelector{Ref: view.Target})
		if err != nil {
			t.Fatal(err)
		}
		if update.Status != "checking" {
			if update.Status != "unchanged" {
				t.Fatalf("source update = %q, want unchanged", update.Status)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("source update check did not settle")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
