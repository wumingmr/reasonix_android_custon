package main

import (
	"context"
	"reflect"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

type workspaceInfoProbe struct{ ids []string }

func (p *workspaceInfoProbe) Stat(_ context.Context, ref session.SessionRef) (session.SessionInfo, error) {
	p.ids = append(p.ids, ref.SessionID)
	return session.SessionInfo{SessionID: ref.SessionID, MetadataStatus: session.MetadataReady}, nil
}

func TestWorkspaceMetadataReadsOnlyRegisteredMembers(t *testing.T) {
	probe := &workspaceInfoProbe{}
	for _, ids := range [][]string{{"a", "b", "a"}, {"c"}, {"d", "e"}} {
		if _, err := listWorkspaceSessionInfo(t.Context(), probe, ids); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(probe.ids, []string{"a", "b", "c", "d", "e"}) {
		t.Fatalf("repeated or unrelated reads: %v", probe.ids)
	}
}

func TestWorkspacePendingMetadataIsNotBlank(t *testing.T) {
	for _, status := range []string{session.MetadataPending, session.MetadataFailed} {
		row := workspaceSessionRow("global", "old", session.SessionInfo{MetadataStatus: status}, true, false, nil)
		if row.Blank {
			t.Fatalf("%s metadata was classified as blank", status)
		}
	}
	row := workspaceSessionRow("global", "missing", session.SessionInfo{}, false, false, nil)
	if row.Blank {
		t.Fatal("missing metadata was classified as blank")
	}
	row = workspaceSessionRow("global", "empty", session.SessionInfo{MetadataStatus: session.MetadataReady}, true, false, nil)
	if !row.Blank {
		t.Fatal("ready empty session should be blank")
	}
	row = workspaceSessionRow("global", "named", session.SessionInfo{MetadataStatus: session.MetadataReady, Title: "Saved title"}, true, false, nil)
	if row.Blank {
		t.Fatal("named session should not be hidden")
	}
}

func TestCanonicalSessionTopicIdentityUsesTargetPresentation(t *testing.T) {
	state := workspacestate.State{Presentation: map[string]workspacestate.Presentation{
		"target": {TopicID: "topic-target", Title: "Target"},
	}}
	topicID, title := canonicalSessionTopicIdentity(state, "target")
	if topicID != "topic-target" || title != "Target" {
		t.Fatalf("identity = %q/%q, want target presentation", topicID, title)
	}
	topicID, title = canonicalSessionTopicIdentity(state, "missing")
	if topicID != "canonical-missing" || title != "" {
		t.Fatalf("fallback identity = %q/%q", topicID, title)
	}
}

func TestCanonicalBindingUsesRenamedSessionTitleAndRuntimeIdentity(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	ws, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: "renamed-binding", Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	ref := runtime.Ref()
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", ws, ref.SessionID, ""); err != nil {
		t.Fatal(err)
	}
	old := "Old fork title"
	if err := app.workspaceRegistry().UpdatePresentation(t.Context(), []string{ref.SessionID}, &old, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.RenameSessionTarget(SessionSelector{Ref: &ref}, "Renamed B"); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{ID: "binding", SessionID: ref.SessionID, HistoricalSource: &SessionSourceRef{Path: "/fixture/old.jsonl"}}
	app.tabs[tab.ID] = tab
	if err := app.commitCanonicalSessionBinding(tab, nil, ref, state.Workspaces[ws], 0); err != nil {
		t.Fatal(err)
	}
	if tab.HistoricalSource != nil {
		t.Fatal("canonical binding retained the preparation action")
	}
	if tab.TopicTitle != "Renamed B" {
		t.Fatalf("reopened title=%q", tab.TopicTitle)
	}
	snapshot := app.GetRuntimeStateSnapshot()
	if len(snapshot.Topics) != 1 || snapshot.Topics[0].Node.Session == nil || snapshot.Topics[0].Node.Session.SessionID != ref.SessionID {
		t.Fatalf("runtime lost canonical identity: %+v", snapshot.Topics)
	}
}
