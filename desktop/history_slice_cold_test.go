package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/provider"
)

func TestColdCompatibilityReadersResolveOriginalGlobalDirectory(t *testing.T) {
	app := historySliceTestApp(t)
	t.Cleanup(app.closeHistoryReaders)
	tab := newColdHistoryTab(t, app)
	tab.WorkspaceRoot = globalWorkspaceRoot()
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	tab.SessionPath = filepath.Join(dir, "global-before-workspaces.jsonl")
	body := []byte("{\"role\":\"user\",\"content\":\"original global history\"}\n")
	if err := os.WriteFile(tab.SessionPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	page := app.HistoryPageForTab(tab.ID, 0, 10)
	if len(page.Messages) != 1 || page.Messages[0].Content != "original global history" {
		t.Fatalf("legacy page lost its original source: %+v", page)
	}
	messages := app.HistoryForTab(tab.ID)
	if len(messages) != 1 || messages[0].Content != "original global history" {
		t.Fatalf("legacy history lost its original source: %+v", messages)
	}
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, body, 0600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{outside, filepath.Join(dir, "escape.jsonl")} {
		if source != outside {
			if err := os.Symlink(outside, source); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := app.historyReadSource(tabSessionDir(tab), source); err == nil {
			t.Fatalf("accepted a source outside known roots: %s", source)
		}
	}
}

func TestHistorySliceColdPathBeforeControllerReady(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	messages := make([]provider.Message, 0, 80)
	for i := range 40 {
		messages = append(messages,
			provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("cold user turn %d large-session marker", i)},
			provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("cold assistant turn %d large-session marker", i)},
		)
	}
	_, path := saveHistorySliceSession(t, dir, "cold-large.jsonl", messages)
	app := NewApp()
	tab := &WorkspaceTab{
		ID:            "cold-large",
		Scope:         "project",
		WorkspaceRoot: root,
		SessionPath:   path,
		Ready:         false,
		Ctrl:          nil,
	}
	app.mu.Lock()
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.activeTabID = tab.ID
	app.mu.Unlock()

	page := app.HistorySliceForTab(tab.ID, HistorySliceRequest{Turns: 12})
	if page.Error != "" {
		t.Fatalf("cold slice error = %q", page.Error)
	}
	if len(page.Entries) == 0 {
		t.Fatal("cold slice returned no entries before controller ready")
	}
	if page.Source != "index" && page.Source != "scan" {
		t.Fatalf("cold source = %q, want index|scan", page.Source)
	}
}

func TestHistorySliceReportsErrorInsteadOfEmptySuccess(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	tab := &WorkspaceTab{
		ID:          "missing",
		Scope:       "project",
		SessionPath: filepath.Join(t.TempDir(), "does-not-exist.jsonl"),
		Ready:       false,
	}
	app.mu.Lock()
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.activeTabID = tab.ID
	app.mu.Unlock()

	page := app.HistorySliceForTab(tab.ID, HistorySliceRequest{})
	if page.Error == "" {
		t.Fatal("missing session should report Error, not a silent empty success")
	}
	if len(page.Entries) != 0 {
		t.Fatalf("entries = %d, want 0 on error", len(page.Entries))
	}
}
