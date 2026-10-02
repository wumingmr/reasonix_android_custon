package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNamedEmptyTopicArchiveIsRecoverable(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.ctx = t.Context()
	installNoopRuntimeEvents(a)
	t.Cleanup(a.closeSessionServices)
	topic, err := a.CreateTopic("global", "", "Named empty conversation")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.TrashTopic(topic.ID); err != nil {
		t.Fatal(err)
	}
	page, err := a.ListTrashEntries("", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.Title == topic.Title && item.CanRestore {
			return
		}
	}
	t.Fatalf("successful move to trash has no restorable entry: items=%+v, title=%q", page.Items, loadTopicTitle("", topic.ID))
}

func TestEmptyTopicArchiveDoesNotReportSuccessWhileStillVisible(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.ctx = t.Context()
	installNoopRuntimeEvents(a)
	t.Cleanup(a.closeSessionServices)
	topic, err := a.CreateTopic("global", "", "Cannot remove metadata")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(desktopConfigDir(), desktopProjectsFile)
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	err = a.TrashTopic(topic.ID)
	if err != nil {
		return
	}
	page, err := a.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range page.Items {
		if row.TopicID == topic.ID {
			t.Fatalf("archive returned success but topic remains visible: %+v", row)
		}
	}
}
