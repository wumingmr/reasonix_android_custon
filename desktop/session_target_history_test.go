package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
)

func writeTargetHistoryFixture(t *testing.T, dir, name, marker string) string {
	t.Helper()
	path := filepath.Join(dir, name+".jsonl")
	session := agent.NewSession("")
	for range 4 {
		session.Add(provider.Message{Role: provider.RoleUser, Content: marker + " user"})
		session.Add(provider.Message{Role: provider.RoleAssistant, Content: marker + " answer"})
	}
	if err := session.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	if err := agent.UpdateBranchMeta(path, false, func(meta *agent.BranchMeta) error {
		meta.Scope = "global"
		meta.TopicID = name
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHistorySliceForColdLegacyTargetDoesNotNavigateAndBindsCursor(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	first := writeTargetHistoryFixture(t, dir, "first", "first")
	second := writeTargetHistoryFixture(t, dir, "second", "second")
	app := NewApp()
	installSessionCatalogForTest(t, app, dir, "global", "")
	app.tabs = map[string]*WorkspaceTab{"active": {ID: "active", TopicID: "unrelated", SessionID: "unrelated"}}
	app.activeTabID = "active"

	page, err := app.HistorySliceForTarget(SessionSelector{SessionPath: first}, HistorySliceRequest{Turns: 1, Entries: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) == 0 || !page.HasOlder || page.NextCursor == "" {
		t.Fatalf("first page = %+v, want bounded page with older cursor", page)
	}
	if app.activeTabID != "active" || app.tabs["active"].TopicID != "unrelated" {
		t.Fatal("cold target history changed active navigation")
	}
	if _, err := app.HistorySliceForTarget(SessionSelector{SessionPath: second}, HistorySliceRequest{Cursor: page.NextCursor, Turns: 1, Entries: 2}); err == nil ||
		!strings.Contains(err.Error(), "session_operation:stale_cursor:") {
		t.Fatalf("cross-target cursor = %v, want stale_cursor", err)
	}
}

func TestCopySessionTargetPreservesColdLegacyHistory(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeTargetHistoryFixture(t, dir, "legacy-copy", "legacy-copy")
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.ctx = t.Context()
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	installSessionCatalogForTest(t, app, dir, "global", "")

	result, err := app.CopySessionTarget(SessionSelector{SessionPath: path}, "legacy-copy-operation")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || result.Ref.SessionID == "" {
		t.Fatalf("legacy copy result = %+v", result)
	}
	history, err := app.desktopSessionService("").Query().History(t.Context(), result.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 8 || history[0].Content != "legacy-copy user" || history[7].Content != "legacy-copy answer" {
		t.Fatalf("legacy copied history = %+v", history)
	}
	retry, err := app.CopySessionTarget(SessionSelector{SessionPath: path}, "legacy-copy-operation")
	if err != nil {
		t.Fatal(err)
	}
	if retry.Ref != result.Ref {
		t.Fatalf("legacy retry created another copy: first=%+v retry=%+v", result.Ref, retry.Ref)
	}
}
