package main

import (
	"context"
	"sync"
	"testing"
)

func TestSwitchWorkspaceReusesInitialConversation(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	installNoopRuntimeEvents(a)
	root, other := t.TempDir(), t.TempDir()
	t.Cleanup(func() { a.shutdown(context.Background()) })
	for _, dir := range []string{root, root, other, root} {
		if _, err := a.SwitchWorkspace(dir); err != nil {
			t.Fatal(err)
		}
	}
	if topics := loadTopicTitles(root); len(topics) != 1 {
		t.Fatalf("opening/retrying the same workspace created %d conversations: %v", len(topics), topics)
	}
	a.shutdown(context.Background())
	reopened := NewApp()
	installNoopRuntimeEvents(reopened)
	t.Cleanup(func() { reopened.shutdown(context.Background()) })
	if _, err := reopened.SwitchWorkspace(root); err != nil {
		t.Fatal(err)
	}
	if topics := loadTopicTitles(root); len(topics) != 1 {
		t.Fatalf("reopening after restart created another conversation: %v", topics)
	}
}

func TestSwitchWorkspaceConcurrentOpenCreatesOneConversation(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	installNoopRuntimeEvents(a)
	root := t.TempDir()
	t.Cleanup(func() { a.shutdown(context.Background()) })
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _, _ = a.SwitchWorkspace(root) })
	}
	wg.Wait()
	if topics := loadTopicTitles(root); len(topics) != 1 {
		t.Fatalf("concurrent workspace open created %d conversations: %v", len(topics), topics)
	}
}
