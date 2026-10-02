package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/sessioncatalog"
)

// A save event indexes a transcript under the global fallback scope when its
// sidecar names none, while archive assigns the owner from the project store
// the file lives in. The row must leave the sidebar it was listed in anyway.
func TestArchivedSourceOwnedByAnotherWorkspaceLeavesSidebar(t *testing.T) {
	for _, heads := range []int{1, 2} {
		name := "single-head"
		if heads == 2 {
			name = "branched"
		}
		t.Run(name, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			workspace := filepath.Join(t.TempDir(), "project")
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := saveProjectsFile(desktopProjectFile{Projects: []desktopProject{{Root: workspace}}}); err != nil {
				t.Fatal(err)
			}
			dir := desktopSessionDir(workspace)
			path := filepath.Join(dir, "20260922-122527.100224400-deepseek-chat.jsonl")
			legacy := agent.NewSession("system")
			legacy.Add(provider.Message{ID: "q0", Role: provider.RoleUser, Content: "question"})
			legacy.Add(provider.Message{ID: "a0", Role: provider.RoleAssistant, Content: "answer"})
			if err := legacy.Save(path); err != nil {
				t.Fatal(err)
			}
			if heads == 2 {
				if _, err := legacy.ForkHead(path, legacy.Snapshot()[1].ID, agent.HeadKindFork, "sibling"); err != nil {
					t.Fatal(err)
				}
				legacy.Add(provider.Message{Role: provider.RoleAssistant, Content: "sibling answer"})
				if err := legacy.Save(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := agent.SaveBranchMeta(path, agent.BranchMeta{TopicID: "topic_unscoped"}); err != nil {
				t.Fatal(err)
			}
			app := NewApp()
			app.ctx = t.Context()
			pinDesktopSessionRoot(t, app)
			installNoopRuntimeEvents(app)
			t.Cleanup(app.closeSessionServices)
			catalog, err := sessioncatalog.Open(context.Background(), sessioncatalog.Options{InMemory: true, DisableRepair: true, MetadataOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			app.sessionCatalog.Store(catalog)
			t.Cleanup(func() { app.stopSessionCatalog(time.Second) })
			globalRows := func() []ProjectNode {
				t.Helper()
				// The desktop's persist observer indexes each save under this fallback.
				if err := catalog.IndexSessionPath(context.Background(), sessioncatalog.DirectoryTarget{Path: dir, Scope: "global"}, path); err != nil {
					t.Fatal(err)
				}
				page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
				if err != nil {
					t.Fatal(err)
				}
				app.ReleaseReadSnapshot(page.SnapshotID)
				return page.Items
			}
			rows := globalRows()
			if len(rows) != heads {
				t.Fatalf("global rows before archive = %+v, want %d", rows, heads)
			}
			for _, row := range rows {
				result, err := app.ArchiveSessionTarget(SessionSelector{Ref: row.Session, Source: row.Source, SessionPath: row.SessionPath})
				if err != nil || !result.Committed {
					t.Fatalf("archive %+v = %+v, %v", row.Source, result, err)
				}
			}
			if left := globalRows(); len(left) != 0 {
				var operationErr *SessionOperationError
				err := app.SetSessionPinned(SessionSelector{Source: left[0].Source, SessionPath: left[0].SessionPath}, true)
				if errors.As(err, &operationErr) {
					t.Fatalf("archived rows stay listed (%d) and refuse every action with %q", len(left), operationErr.Code)
				}
				t.Fatalf("archived rows stay listed: %+v", left)
			}
		})
	}
}
