package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/checkpoint"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
)

type tabScopedActionController struct {
	stubSessionAPI
	history           []provider.Message
	newSessionCalls   int
	clearSessionCalls int
	rewindCalls       int
	compactCalls      int
	forkCalls         int
	summarizeFrom     int
	summarizeUpTo     int
}

func newTabScopedActionController() *tabScopedActionController {
	return &tabScopedActionController{history: []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "keep this tab distinct"},
	}}
}

func (c *tabScopedActionController) RuntimeStatus() control.RuntimeStatus {
	return control.RuntimeStatus{}
}
func (c *tabScopedActionController) PlanMode() bool         { return false }
func (c *tabScopedActionController) AutoApproveTools() bool { return false }
func (c *tabScopedActionController) Goal() string           { return "" }
func (c *tabScopedActionController) ToolApprovalMode() string {
	return control.ToolApprovalAsk
}
func (c *tabScopedActionController) History() []provider.Message {
	return append([]provider.Message(nil), c.history...)
}
func (c *tabScopedActionController) WorkspaceRoot() string { return "" }
func (c *tabScopedActionController) SessionDir() string    { return "" }
func (c *tabScopedActionController) SessionPath() string   { return "" }
func (c *tabScopedActionController) Snapshot() error       { return nil }
func (c *tabScopedActionController) SetDisplayRecorder(func(content, display string)) {
}
func (c *tabScopedActionController) NewSession() error {
	c.newSessionCalls++
	c.history = []provider.Message{{Role: provider.RoleSystem, Content: "system"}}
	return nil
}
func (c *tabScopedActionController) ClearSession() error {
	c.clearSessionCalls++
	c.history = []provider.Message{{Role: provider.RoleSystem, Content: "system"}}
	return nil
}
func (c *tabScopedActionController) Rewind(_ int, _ control.RewindScope) error {
	c.rewindCalls++
	return nil
}
func (c *tabScopedActionController) PrepareRewind(_ int, _ control.RewindScope) (checkpoint.RewindPlan, error) {
	return checkpoint.RewindPlan{PlanID: "tab-scoped", CanFiles: true, CanConversation: true}, nil
}
func (c *tabScopedActionController) CommitRewind(_ string) (checkpoint.RewindResult, error) {
	c.rewindCalls++
	return checkpoint.RewindResult{OK: true}, nil
}
func (c *tabScopedActionController) Compact(_ context.Context, _ string) error {
	c.compactCalls++
	return nil
}
func (c *tabScopedActionController) ForkSession(_ int, _ string) (string, error) {
	c.forkCalls++
	return "", errors.New("stop after selecting source controller")
}
func (c *tabScopedActionController) SummarizeFrom(_ context.Context, _ int) error {
	c.summarizeFrom++
	return nil
}
func (c *tabScopedActionController) SummarizeUpTo(_ context.Context, _ int) error {
	c.summarizeUpTo++
	return nil
}

type blockingForkTabController struct {
	*tabScopedActionController
	path    string
	started chan struct{}
	release chan struct{}
}

func (c *blockingForkTabController) ForkSession(_ int, _ string) (string, error) {
	close(c.started)
	<-c.release
	return c.path, nil
}

func TestTabScopedSessionActionsIgnoreFocusedTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	targetCtrl := newTabScopedActionController()
	focusedCtrl := newTabScopedActionController()
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"target":  {ID: "target", Scope: "global", Ready: true, Ctrl: targetCtrl, disabledMCP: map[string]ServerView{}},
			"focused": {ID: "focused", Scope: "global", Ready: true, Ctrl: focusedCtrl, disabledMCP: map[string]ServerView{}},
		},
		tabOrder:    []string{"target", "focused"},
		activeTabID: "focused",
	}

	if err := app.NewSessionForTab("target"); err != nil {
		t.Fatalf("NewSessionForTab: %v", err)
	}
	// Restore content so ClearSession exercises its non-blank path too.
	targetCtrl.history = []provider.Message{{Role: provider.RoleSystem, Content: "system"}, {Role: provider.RoleUser, Content: "clear me"}}
	if _, err := app.ClearSessionForTab("target"); err != nil {
		t.Fatalf("ClearSessionForTab: %v", err)
	}
	if err := app.RewindForTab("target", 1, "conversation"); err != nil {
		t.Fatalf("RewindForTab: %v", err)
	}
	if err := app.CompactForTab("target"); err != nil {
		t.Fatalf("CompactForTab: %v", err)
	}
	if _, err := app.ForkForTab("target", 1); err == nil || err.Error() != "stop after selecting source controller" {
		t.Fatalf("ForkForTab error = %v, want source-controller sentinel", err)
	}
	if err := app.SummarizeFromForTab("target", 1); err != nil {
		t.Fatalf("SummarizeFromForTab: %v", err)
	}
	if err := app.SummarizeUpToForTab("target", 1); err != nil {
		t.Fatalf("SummarizeUpToForTab: %v", err)
	}

	if targetCtrl.newSessionCalls != 1 || targetCtrl.clearSessionCalls != 1 || targetCtrl.rewindCalls != 1 || targetCtrl.compactCalls != 1 || targetCtrl.forkCalls != 1 || targetCtrl.summarizeFrom != 1 || targetCtrl.summarizeUpTo != 1 {
		t.Fatalf("target calls = new:%d clear:%d rewind:%d compact:%d fork:%d from:%d upto:%d, want all 1",
			targetCtrl.newSessionCalls, targetCtrl.clearSessionCalls, targetCtrl.rewindCalls, targetCtrl.compactCalls, targetCtrl.forkCalls, targetCtrl.summarizeFrom, targetCtrl.summarizeUpTo)
	}
	if focusedCtrl.newSessionCalls != 0 || focusedCtrl.clearSessionCalls != 0 || focusedCtrl.rewindCalls != 0 || focusedCtrl.compactCalls != 0 || focusedCtrl.forkCalls != 0 || focusedCtrl.summarizeFrom != 0 || focusedCtrl.summarizeUpTo != 0 {
		t.Fatalf("focused tab received scoped action: %+v", focusedCtrl)
	}
	if app.activeTabID != "focused" {
		t.Fatalf("active tab = %q, want focused", app.activeTabID)
	}
}

func TestForkForTabDoesNotOverrideLaterActiveTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	ctrl := &blockingForkTabController{
		tabScopedActionController: newTabScopedActionController(),
		path:                      filepath.Join(config.SessionDir(), "fork.jsonl"),
		started:                   make(chan struct{}),
		release:                   make(chan struct{}),
	}
	// Only the persistence-to-publish interleaving matters here; fork itself
	// need not run in another goroutine or race a wall-clock deadline.
	close(ctrl.release)
	app := NewApp()
	app.ctx = context.Background()
	app.setTestCtrl(ctrl, "")
	app.tabs["test"].TopicTitle = "Source topic"
	app.tabs["other"] = &WorkspaceTab{ID: "other", Scope: "global", Ready: true, disabledMCP: map[string]ServerView{}}
	app.tabOrder = []string{"test", "other"}

	// Match the app's asynchronous build path, but cancel and join the build
	// before it acquires controllers or session leases unrelated to this test.
	buildRelease := make(chan struct{})
	app.tabBuildStartHook = func(string) { <-buildRelease }
	t.Cleanup(func() {
		app.mu.Lock()
		var builds []<-chan struct{}
		for _, tab := range app.tabs {
			if tab.buildDone != nil {
				builds = append(builds, tab.buildDone)
			}
			app.supersedeTabBuildLocked(tab)
		}
		app.mu.Unlock()
		close(buildRelease)
		for _, done := range builds {
			if !waitChannelBefore(done, time.Now().Add(5*time.Second)) {
				t.Error("fork controller build did not exit after cancellation")
			}
		}
	})
	hook := func() {
		if err := app.SetActiveTab("other"); err != nil {
			t.Fatalf("SetActiveTab(other): %v", err)
		}
	}
	forkTabBeforePublishHookForTest.Store(&hook)
	t.Cleanup(func() { forkTabBeforePublishHookForTest.Store(nil) })

	meta, err := app.ForkForTab("test", 1)
	if err != nil {
		t.Fatalf("ForkForTab: %v", err)
	}
	if meta.ID == "" || meta.ID == "test" {
		t.Fatalf("fork tab ID = %q, want a fresh tab", meta.ID)
	}
	if meta.Active {
		t.Fatalf("fork meta active = true, want false after later tab switch")
	}
	if app.activeTabID != "other" {
		t.Fatalf("active tab = %q, want later selection %q", app.activeTabID, "other")
	}
}
