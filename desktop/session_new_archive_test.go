package main

import (
	"os"
	"testing"

	"reasonix/internal/session"
)

func TestNewBlankSessionCanBeArchived(t *testing.T) {
	for _, scope := range []string{"global", "project"} {
		t.Run(scope, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			a := NewApp()
			a.ctx = t.Context()
			installNoopRuntimeEvents(a)
			t.Cleanup(a.closeSessionServices)
			root := ""
			if scope == "project" {
				root = t.TempDir()
			}
			meta, err := a.EnsureBlankTab(scope, root)
			if err != nil {
				t.Fatal(err)
			}
			ref := session.SessionRef{HostID: localDesktopHostID, SessionID: meta.SessionID}
			result, err := a.ArchiveSessionTarget(SessionSelector{Ref: &ref})
			if err != nil || !result.Committed {
				t.Fatalf("archive new blank session: %+v, %v", result, err)
			}
			assertNoVisibleRuntime(t, a)
			page, err := a.ListProjectTopics(ProjectTopicPageRequest{Scope: scope, WorkspaceRoot: root, Limit: 50})
			if err != nil || len(page.Items) != 0 {
				t.Fatalf("archived blank remains in sidebar: %+v, %v", page, err)
			}
		})
	}
}

func TestNewUnopenedTopicCanBeArchived(t *testing.T) {
	for _, scope := range []string{"global", "project"} {
		t.Run(scope, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			a := NewApp()
			a.ctx = t.Context()
			installNoopRuntimeEvents(a)
			t.Cleanup(a.closeSessionServices)
			root := ""
			if scope == "project" {
				root = t.TempDir()
			}
			topic, err := a.CreateTopic(scope, root, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := a.TrashTopic(topic.ID); err != nil {
				t.Fatalf("archive unopened blank topic: %v", err)
			}
			assertNoVisibleRuntime(t, a)
			if topicIndexedInRegistry(scope, root, topic.ID) || loadTopicTitle(root, topic.ID) != "" {
				t.Fatal("archived topic remains in metadata")
			}
			if !containsDesktopString(loadProjectsFile().DeletedTopics, topic.ID) {
				t.Fatal("missing tombstone for archived empty topic")
			}
			if _, err := os.Stat(topicArchiveMetadataPendingPath(topic.ID)); !os.IsNotExist(err) {
				t.Fatalf("metadata cleanup remains pending: %v", err)
			}
			// A fresh application must not reconstruct the row from legacy metadata.
			restarted := NewApp()
			restarted.ctx = t.Context()
			t.Cleanup(restarted.closeSessionServices)
			page, err := restarted.ListProjectTopics(ProjectTopicPageRequest{Scope: scope, WorkspaceRoot: root, Limit: 50})
			if err != nil || len(page.Items) != 0 {
				t.Fatalf("archived topic reappeared after restart: %+v, %v", page, err)
			}
		})
	}
}

func TestNewFailedTopicCanBeArchived(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.ctx = t.Context()
	installNoopRuntimeEvents(a)
	t.Cleanup(a.closeSessionServices)
	topic, err := a.CreateTopic("global", "", "")
	if err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{ID: "failed", Scope: "global", WorkspaceRoot: globalWorkspaceRoot(), TopicID: topic.ID, StartupErr: "build failed"}
	a.tabs[tab.ID] = tab
	a.tabOrder = []string{tab.ID}
	a.activeTabID = tab.ID
	if err := a.TrashTopic(topic.ID); err != nil {
		t.Fatalf("archive failed new topic: %v", err)
	}
	assertNoVisibleRuntime(t, a)
	if !tab.removed {
		t.Fatal("failed startup can still publish the removed topic")
	}
}
