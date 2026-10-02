package main

import (
	"testing"
)

const backgroundRemoteTestPath = "/sessions/background.jsonl"

// backgroundFreshness reports the freshness the runtime snapshot assigns to
// the tab's background route.
func backgroundFreshness(t *testing.T, a *App, tab *remoteTab) string {
	t.Helper()
	for _, session := range a.sampleRemoteRuntimeSessions() {
		if session.TabID == tab.id && session.SessionPath == backgroundRemoteTestPath {
			return session.Freshness
		}
	}
	t.Fatalf("background route %q missing from the runtime snapshot", backgroundRemoteTestPath)
	return ""
}

// runtimeStates are seeded once and never cleared on suspend or park, so a
// tab that lost its stream keeps whatever phase it last observed. Reporting
// that frozen snapshot as "synced" tells the project tree a session is still
// executing after the tunnel dropped.
func TestBackgroundRouteFreshnessFollowsTheTabConnection(t *testing.T) {
	a, tab := remoteRuntimeTestApp(nil)
	executing := remoteRuntimeTestSnapshot("epoch-1", 4, "executing")
	acceptRemoteRuntimeStateLocked(tab, backgroundRemoteTestPath, executing, true)
	if got := backgroundFreshness(t, a, tab); got != "synced" {
		t.Fatalf("freshly observed background route = %q, want synced", got)
	}

	tab.state = "reconnecting"
	if got := backgroundFreshness(t, a, tab); got != "unknown" {
		t.Fatalf("disconnected tab background route = %q, want unknown", got)
	}

	tab.state = "ready"
	tab.runtime.syncFailed = true
	if got := backgroundFreshness(t, a, tab); got != "unknown" {
		t.Fatalf("failed-sync background route = %q, want unknown", got)
	}

	// A foreground ownership change says nothing about another session: the
	// stream is live and the background snapshot is still being observed.
	tab.runtime.syncFailed = false
	tab.session.takenOver = true
	if got := backgroundFreshness(t, a, tab); got != "synced" {
		t.Fatalf("taken-over foreground invalidated the background route: %q", got)
	}
}
