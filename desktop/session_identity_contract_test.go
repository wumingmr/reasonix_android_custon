package main

import "testing"

func TestBumpAndSnapshotSessionClearCarriesCanonicalSessionRef(t *testing.T) {
	tab := &WorkspaceTab{ID: "tab-canonical", SessionID: "canonical-a", SessionGeneration: 4}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}}

	result := app.bumpAndSnapshotSessionClear(tab)

	if result.SessionPath != "" {
		t.Fatalf("session path = %q, want empty canonical compatibility path", result.SessionPath)
	}
	if result.SessionID != "canonical-a" {
		t.Fatalf("session id = %q, want canonical-a", result.SessionID)
	}
	if result.Session == nil || result.Session.HostID != localDesktopHostID || result.Session.SessionID != "canonical-a" {
		t.Fatalf("session ref = %#v, want local/canonical-a", result.Session)
	}
	if result.SessionGeneration != 5 {
		t.Fatalf("session generation = %d, want 5", result.SessionGeneration)
	}
}
