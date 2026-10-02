package main

import "testing"

func TestTabLayoutQueueFlushKeepsLatestVersion(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	dir := t.TempDir()
	app.tabsSaveMu.Lock()
	for version := uint64(1); version <= 100; version++ {
		app.queueTabLayoutSave(dir, []desktopTabEntry{}, "latest", version)
	}
	app.tabsSaveMu.Unlock()
	app.flushTabLayoutWrites()
	if app.tabsLastWrittenVersion != 100 {
		t.Fatalf("saved version=%d", app.tabsLastWrittenVersion)
	}
	app.queueTabLayoutSave(dir, []desktopTabEntry{}, "obsolete", 99)
	app.saveTabsWrite(dir, []desktopTabEntry{}, "newer", 101)
	app.flushTabLayoutWrites()
	if app.tabsLastWrittenVersion != 101 {
		t.Fatal("queued layout overwrote a newer synchronous save")
	}
	app.desktopSessions.layoutWrites.mu.Lock()
	defer app.desktopSessions.layoutWrites.mu.Unlock()
	if app.desktopSessions.layoutWrites.done != nil {
		t.Fatal("flush left a layout worker running")
	}
}
