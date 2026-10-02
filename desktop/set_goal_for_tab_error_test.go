package main

import (
	"errors"
	"strings"
	"testing"

	"reasonix/internal/control"
)

type rejectingGoalSession struct {
	control.SessionAPI
	err error
}

func (s *rejectingGoalSession) SetGoalDurable(string) error           { return s.err }
func (s *rejectingGoalSession) EditGoalDurable(string, *uint64) error { return s.err }

func TestSetGoalForTabReturnsErrorWhenTabMissing(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{}
	app.tabOrder = nil
	app.activeTabID = ""

	err := app.SetGoalForTab("missing-tab", "ship the goal skill path")
	if err == nil {
		t.Fatal("SetGoalForTab with a missing tab returned nil error")
	}
	if !strings.Contains(err.Error(), "workspace is still starting") {
		t.Fatalf("SetGoalForTab missing-tab error = %v, want workspace-not-ready wording", err)
	}
}

func TestClearGoalForTabReturnsErrorWhenTabMissing(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{}
	app.tabOrder = nil
	app.activeTabID = ""

	err := app.ClearGoalForTab("gone")
	if err == nil {
		t.Fatal("ClearGoalForTab with a missing tab returned nil error")
	}
	if !strings.Contains(err.Error(), "workspace is still starting") {
		t.Fatalf("ClearGoalForTab missing-tab error = %v, want workspace-not-ready wording", err)
	}
}

func TestSetGoalForTabSucceedsAndPersistsLocalGoal(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	tab := testTab("a", t.TempDir())
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	defer tab.Ctrl.Close()

	if err := app.SetGoalForTab(tab.ID, "finish the goal skill path"); err != nil {
		t.Fatalf("SetGoalForTab: %v", err)
	}
	if tab.goal != "finish the goal skill path" {
		t.Fatalf("tab.goal = %q, want set", tab.goal)
	}
	if tab.Ctrl.Goal() != "finish the goal skill path" {
		t.Fatalf("controller goal = %q, want set", tab.Ctrl.Goal())
	}

	if err := app.ClearGoalForTab(tab.ID); err != nil {
		t.Fatalf("ClearGoalForTab: %v", err)
	}
	if tab.goal != "" {
		t.Fatalf("cleared tab.goal = %q, want empty", tab.goal)
	}
	if tab.Ctrl.Goal() != "" {
		t.Fatalf("cleared controller goal = %q, want empty", tab.Ctrl.Goal())
	}
}

func TestSetGoalForTabDoesNotPublishMetadataWhenPersistenceFails(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	tab := testTab("a", t.TempDir())
	base := tab.Ctrl
	tab.Ctrl = &rejectingGoalSession{SessionAPI: base, err: errors.New("disk full")}
	tab.goal = "existing goal"
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	defer base.Close()

	err := app.SetGoalForTab(tab.ID, "replacement goal")
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("SetGoalForTab error = %v, want persistence failure", err)
	}
	if tab.goal != "existing goal" {
		t.Fatalf("tab goal = %q, want unchanged metadata", tab.goal)
	}
	if base.Goal() != "" {
		t.Fatalf("controller goal = %q, want no accepted mutation", base.Goal())
	}
}

func TestEditGoalForTabDoesNotPublishMetadataWhenPersistenceFails(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	tab := testTab("a", t.TempDir())
	base := tab.Ctrl
	tab.Ctrl = &rejectingGoalSession{SessionAPI: base, err: errors.New("disk full")}
	tab.goal = "existing goal"
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	defer base.Close()

	limit := uint64(9)
	err := app.EditGoalForTab(tab.ID, "revised goal", &limit)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("EditGoalForTab error = %v, want persistence failure", err)
	}
	if tab.goal != "existing goal" {
		t.Fatalf("tab goal = %q, want unchanged metadata", tab.goal)
	}
}
