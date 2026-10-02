package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestIndependentForkResumesPublicationWithSameOperationWithoutResettingChoices(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-process-retry", true: "restart-recovery"}[restart], func(t *testing.T) { independentForkPublicationRecovery(t, restart) })
	}
}

func independentForkPublicationRecovery(t *testing.T, restart bool) {
	app, root, refs := canonicalOrganizationFixture(t, "parent", "sibling")
	parentRef, siblingRef := refs["parent"], refs["sibling"]
	parentKey := projectNodeSessionKey(ProjectNode{Session: &parentRef})
	siblingKey := projectNodeSessionKey(ProjectNode{Session: &siblingRef})
	if err := app.ReorderSessions("project", root, []string{parentKey, siblingKey}); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveSessionGroups("project", root, []desktopGroup{{ID: "parent-group", Title: "Parent group", SessionKeys: []string{parentKey}}}); err != nil {
		t.Fatal(err)
	}
	service := app.desktopSessionService("")
	parent, ok := service.Runtime(refs["parent"])
	if !ok {
		t.Fatal("parent runtime missing")
	}
	payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "retained fork history"}})
	if _, err := parent.Session().Append(t.Context(), session.Batch{OperationID: "turn-1", TurnID: "turn-1", Events: []session.Event{
		{Kind: "turn/start"}, {Kind: "message/complete", Payload: payload}, {Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	plan, err := app.planCanonicalFork(refs["parent"], "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	operationID, childID := "publication-retry", "publication-child"
	// Force the real persistence -> registry publication race without timing or
	// a production test hook: the captured plan is now one lifecycle behind.
	if err := app.workspaceRegistry().ArchiveSession(t.Context(), refs["parent"].SessionID); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().RestoreSession(t.Context(), refs["parent"].SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.executeCanonicalFork(refs["parent"], plan, operationID, childID); !errors.Is(err, workspacestate.ErrMutationConflict) {
		t.Fatalf("stale publication error = %v, want mutation conflict", err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if pending, ok := state.PendingCreates[childID]; !ok || pending.OperationID != operationID {
		t.Fatalf("pending publication lost: %#v", state.PendingCreates)
	}
	childRef := session.SessionRef{HostID: "local", SessionID: childID}
	if _, err := service.Query().Snapshot(t.Context(), childRef); err != nil {
		t.Fatalf("failed publication lost durable child: %v", err)
	}
	if _, attached := state.SessionStates[childID]; attached {
		t.Fatal("failed publication attached child")
	}
	if restart {
		serviceRoot, registryPath := app.desktopSessions.root, app.workspaceRegistry().Path()
		app.closeSessionServices()
		app = NewApp()
		app.ctx = t.Context()
		app.desktopSessions.root = serviceRoot
		app.desktopSessions.workspaceState = workspacestate.NewStore(registryPath)
		t.Cleanup(app.closeSessionServices)
		if err := app.recoverDesktopPendingCreates(t.Context()); err != nil {
			t.Fatal(err)
		}
		state, err = app.workspaceRegistry().Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"parent", childID, "sibling"}; !reflect.DeepEqual(state.Workspaces[plan.workspaceID].SessionIDs, want) {
			t.Fatalf("recovery order=%v want=%v", state.Workspaces[plan.workspaceID].SessionIDs, want)
		}
		organization, err := app.GetSessionOrganization(SessionOrganizationWorkspace{Scope: "project", WorkspaceRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		if len(organization.Groups) != 1 || !reflect.DeepEqual(organization.Groups[0].SessionKeys, []string{parentKey, projectNodeSessionKey(ProjectNode{Session: &childRef})}) {
			t.Fatalf("recovery lost inherited group: %#v", organization.Groups)
		}
	}
	plan, err = app.planCanonicalFork(refs["parent"], "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	child, err := app.executeCanonicalFork(refs["parent"], plan, operationID, childID)
	if err != nil {
		t.Fatal(err)
	}
	if child != childRef {
		t.Fatalf("retry changed child identity: %#v", child)
	}
	if _, err := app.RenameSessionTarget(SessionSelector{Ref: &childRef}, "User chosen child title"); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveSessionGroups("project", root, []desktopGroup{{ID: "chosen", Title: "Chosen", SessionKeys: []string{projectNodeSessionKey(ProjectNode{Session: &childRef})}}}); err != nil {
		t.Fatal(err)
	}
	if err := app.SetSessionPinned(SessionSelector{Ref: &childRef}, true); err != nil {
		t.Fatal(err)
	}
	before, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.executeCanonicalFork(refs["parent"], plan, operationID, childID); err != nil {
		t.Fatal(err)
	}
	after, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := after.PendingCreates[childID]; exists {
		t.Fatal("retry left pending publication")
	}
	if !reflect.DeepEqual(before.Presentation[childID], after.Presentation[childID]) {
		t.Fatalf("retry reset user presentation: %#v -> %#v", before.Presentation[childID], after.Presentation[childID])
	}
	if !reflect.DeepEqual(before.Workspaces[plan.workspaceID].Organization, after.Workspaces[plan.workspaceID].Organization) {
		t.Fatal("retry reset user group/order")
	}
	count := 0
	for _, id := range after.Workspaces[plan.workspaceID].SessionIDs {
		if id == childID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("child membership count=%d", count)
	}
}

func TestIndependentSessionForkRespectsManualSidebarOrder(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.ctx = t.Context()
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	if err := addProject(root, "Fork project"); err != nil {
		t.Fatal(err)
	}
	parent, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{
		SessionID: "target-fork-parent", CWD: root, Origin: session.SessionOriginNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"message": provider.Message{
		ID: "answer", Role: provider.RoleAssistant, Content: "forked target history",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Session().Append(t.Context(), session.Batch{
		OperationID: "turn-1", TurnID: "turn-1",
		Events: []session.Event{
			{Kind: "turn/start"},
			{Kind: "message/complete", Payload: payload},
			{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := app.desktopSessions.workspaceState.AttachSession(t.Context(), "", workspaceID, parent.Ref().SessionID, ""); err != nil {
		t.Fatal(err)
	}
	if err := app.desktopSessionService("").SetTitle(t.Context(), parent.Ref(), "Parent work"); err != nil {
		t.Fatal(err)
	}
	parentTitle, parentPinned := "Parent work", true
	if err := app.workspaceRegistry().UpdatePresentation(t.Context(), []string{parent.Ref().SessionID}, &parentTitle, &parentPinned); err != nil {
		t.Fatal(err)
	}
	parentRef := parent.Ref()
	if err := app.SaveSessionGroups("project", root, []desktopGroup{{
		ID: "feature", Title: "Feature", SessionKeys: []string{projectNodeSessionKey(ProjectNode{Session: &parentRef})},
	}}); err != nil {
		t.Fatal(err)
	}
	app.tabs = map[string]*WorkspaceTab{"active": {ID: "active", SessionID: "unrelated"}}
	app.activeTabID = "active"

	sibling, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: "sibling", CWD: root, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspaceID, sibling.Ref().SessionID, ""); err != nil {
		t.Fatal(err)
	}
	parentKey := projectNodeSessionKey(ProjectNode{Session: &parentRef})
	siblingRef := sibling.Ref()
	siblingKey := projectNodeSessionKey(ProjectNode{Session: &siblingRef})
	if err := app.ReorderSessions("project", root, []string{parentKey, siblingKey}); err != nil {
		t.Fatal(err)
	}

	selector := SessionSelector{Ref: &session.SessionRef{HostID: localDesktopHostID, SessionID: parent.Ref().SessionID}}
	first, err := app.ForkSessionTarget(selector, "turn-1")
	if err != nil {
		t.Fatal(err)
	}

	page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range page.Items {
		if row.Session != nil {
			got = append(got, row.Session.SessionID)
		}
	}
	want := []string{parent.Ref().SessionID, first.SessionID, "sibling"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sidebar order = %v; want parent, child, sibling = %v", got, want)
	}
}

func TestIndependentSessionOrderKeepsFrozenCursor(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.ctx = t.Context()
	app.desktopSessions.root = filepath.Join(root, "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "state.json"))
	ws, err := app.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	if err := addProject(root, "Review"); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, id := range []string{"a", "b", "c"} {
		runtime, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: id, CWD: root, Origin: session.SessionOriginNew})
		if err != nil {
			t.Fatal(err)
		}
		if err := app.workspaceRegistry().AttachSession(t.Context(), "", ws, id, ""); err != nil {
			t.Fatal(err)
		}
		ref := runtime.Ref()
		keys = append(keys, projectNodeSessionKey(ProjectNode{Session: &ref}))
	}
	if err := app.ReorderSessions("project", root, keys); err != nil {
		t.Fatal(err)
	}
	first, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == "" || len(first.Items) != 1 || first.Items[0].Session == nil || first.Items[0].Session.SessionID != "a" {
		t.Fatalf("first page = %#v; want A and a next-page cursor", first)
	}
	if err := app.ReorderSessions("project", root, []string{keys[2], keys[0], keys[1]}); err != nil {
		t.Fatal(err)
	}
	second, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.Items[0].Session.SessionID != "b" || second.SnapshotID != first.SnapshotID {
		t.Fatalf("reorder disturbed frozen continuation: %+v %v", second, err)
	}
	refreshed, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range refreshed.Items {
		if row.Session != nil {
			got = append(got, row.Session.SessionID)
		}
	}
	if want := []string{"c", "a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("refreshed order = %v, want %v", got, want)
	}
}
