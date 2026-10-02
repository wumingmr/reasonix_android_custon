package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// remoteListingTab installs one ready remote tab bound to route, with the given
// serve listing behind it, and returns the app plus the live tab.
func remoteListingTab(t *testing.T, fs *fakeServe, route string) (*App, *remoteTab) {
	t.Helper()
	client, _ := remoteSessionTestClient(t, fs)
	tab := &remoteTab{
		id: "remote-1", ref: RemoteTabRef{HostID: "box", Workspace: "~/app"}, state: "ready",
		client: client, base: fs.server.URL, gen: 3, topicTitle: "Fresh topic",
		routing: remoteTabSessionRouting{currentPath: route, running: map[string]bool{}},
	}
	if _, sessionID := remoteSessionRouteIdentity(route); sessionID != "" {
		tab.session.sessionID = sessionID
	} else {
		tab.session.path = route
	}
	return &App{remoteTabs: map[string]*remoteTab{tab.id: tab}}, tab
}

// The synthetic current row stands in for a foreground session /sessions does
// not list yet. Built from an identity route it must expose that identity as
// sessionId: reporting it as Path makes the resume body ask Serve to resolve
// "session-id:X" as a filesystem path, which fails with 400.
func TestSyntheticCurrentRowCarriesIdentityAsSessionID(t *testing.T) {
	seedBridgeTestHost(t, "box")
	fs := newFakeServe(t, "s3cret", nil)
	a, _ := remoteListingTab(t, fs, remoteSessionIDRoutePrefix+"fresh-id")

	sessions, err := a.RemoteProjectSessions("box", "~/app")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || !sessions[0].Current {
		t.Fatalf("sessions = %+v, want one synthetic current row", sessions)
	}
	row := sessions[0]
	if row.SessionID != "fresh-id" || row.Path != "" {
		t.Fatalf("synthetic row identity = sessionId:%q path:%q, want the identity route as sessionId", row.SessionID, row.Path)
	}

	// The row round-trips through the click path: the frontend hands its
	// fields back verbatim, and the resume body must name the identity.
	body, err := remoteSessionResumeBody(serveSessionEntry{Name: row.Name, Path: row.Path, SessionID: row.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]string
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	if request["sessionId"] != "fresh-id" || request["path"] != "" {
		t.Fatalf("resume body = %s, want the identity in sessionId and an empty path", body)
	}
}

// A legacy path route keeps its path: only identity routes move to sessionId.
func TestSyntheticCurrentRowKeepsLegacyPathRoute(t *testing.T) {
	seedBridgeTestHost(t, "box")
	fs := newFakeServe(t, "s3cret", nil)
	a, _ := remoteListingTab(t, fs, "/sessions/legacy.jsonl")

	sessions, err := a.RemoteProjectSessions("box", "~/app")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Path != "/sessions/legacy.jsonl" || sessions[0].SessionID != "" {
		t.Fatalf("legacy synthetic row = %+v, want the path preserved", sessions)
	}
}

// An ID-only selection (the synthetic identity row has no name) must resume by
// the committed identity instead of falling through to a listing lookup keyed
// on the empty name, which reports "not found".
func TestResumeAcceptsCommittedIdentityWithoutAName(t *testing.T) {
	seedBridgeTestHost(t, "box")
	fs := newFakeServe(t, "s3cret", []serveSessionEntry{{Name: "other", Path: "/other.jsonl", Current: true}})
	a, tab := remoteListingTab(t, fs, remoteSessionIDRoutePrefix+"committed-id")

	if !a.resumeRemoteTabSessionPathForOpenSelection(tab.id, "", "", "", 0, nil) {
		t.Fatal("ID-only selection was rejected")
	}
	if got := fs.resumedSessionID(); got != "committed-id" {
		t.Fatalf("resume carried sessionId %q, want the committed identity", got)
	}
	a.remoteTabMu.Lock()
	state, route, title, errText := tab.state, tab.routing.currentPath, tab.topicTitle, tab.err
	a.remoteTabMu.Unlock()
	if state != "ready" || errText != "" {
		t.Fatalf("ID-only resume state/err = %q/%q, want a clean ready tab", state, errText)
	}
	if route != remoteSessionIDRoutePrefix+"committed-id" {
		t.Fatalf("resumed route = %q, want the committed identity route", route)
	}
	if strings.TrimSpace(title) == "" {
		t.Fatal("ID-only resume left the tab without a display title")
	}
}
