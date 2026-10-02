package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
)

// reloadRuntimeFixture writes the config the ReloadRuntime tests share (one
// configured provider) and returns the isolated session dir.
func reloadRuntimeFixture(t *testing.T) string {
	t.Helper()
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	return dir
}

func reloadRuntimeTab(t *testing.T, app *App, dir string, oldCtrl *control.Controller) *WorkspaceTab {
	t.Helper()
	tab := &WorkspaceTab{
		ID:          "tab_a",
		Scope:       "global",
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_a", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		tab.releaseSessionLease()
		if tab.Ctrl != nil {
			tab.Ctrl.Close()
		}
	})
	return tab
}

// TestReloadRuntimeSwapsAndClosesOldAfterSwap covers the success path through
// boot.Rebuild: the tab's controller is replaced, the session (grants, file)
// migrates, the outgoing controller is closed only after the swap published
// the replacement, and the frontend fence is emitted.
// TestSubmitReloadRoutesToRuntimeReload pins the desktop slash-command entry:
// /reload is a local management action, not a user turn sent to the model.
// TestReloadRuntimeFailureKeepsOldController: a failed build leaves the tab on
// the outgoing controller, which stays open.
func TestReloadRuntimeFailureKeepsOldController(t *testing.T) {
	isolateDesktopUserDirs(t)

	// No providers at all: the build cannot resolve the tab's model.
	cfg := config.Default()
	cfg.DefaultModel = ""
	cfg.Providers = []config.ProviderEntry{}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	closed := false
	oldCtrl := control.New(control.Options{
		SessionDir:  dir,
		SessionPath: filepath.Join(dir, "old.jsonl"),
		Label:       "old",
		Sink:        event.Discard,
		Cleanup:     func() { closed = true },
	})
	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	tab := reloadRuntimeTab(t, app, dir, oldCtrl)

	if err := app.ReloadRuntime(tab.ID); err == nil {
		t.Fatal("ReloadRuntime with an unresolvable model returned nil error")
	}
	if tab.Ctrl != oldCtrl {
		t.Fatal("failed reload replaced the tab controller")
	}
	if closed {
		t.Fatal("failed reload closed the outgoing controller")
	}
	if app.deferredRebuildPending(tab.ID) {
		t.Fatal("hard failure was queued for retry")
	}
}

// TestReloadRuntimeBusyQueuesDeferred covers the busy contract: active work
// queues exactly one reload on the deferred-rebuild loop (coalesced), and the
// retry runs the boot.Rebuild reload once the tab is idle.
