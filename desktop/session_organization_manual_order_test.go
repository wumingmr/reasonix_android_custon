package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

func TestManualOrderStartsFromShownOrderAndKeepsNewSessionsInFirstPage(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.ctx = t.Context()
	installNoopRuntimeEvents(app)
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	create := func(id string) {
		t.Helper()
		if _, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: id, CWD: root, Origin: session.SessionOriginNew}); err != nil {
			t.Fatal(err)
		}
		if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspaceID, id, ""); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	shown := func(limit int) []string {
		t.Helper()
		page, err := app.unifiedProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: limit, SortMode: "updated"})
		if err != nil {
			t.Fatal(err)
		}
		app.ReleaseReadSnapshot(page.SnapshotID)
		ids := []string{}
		for _, row := range page.Items {
			if row.Session == nil {
				t.Fatalf("row without a session identity: %#v", row)
			}
			ids = append(ids, row.Session.SessionID)
		}
		return ids
	}
	ids := []string{}
	for i := range 12 {
		id := fmt.Sprintf("history-%02d", i)
		create(id)
		ids = append([]string{id}, ids...)
	}
	if got := shown(12); !reflect.DeepEqual(got, ids) {
		t.Fatalf("activity order = %v, want %v", got, ids)
	}

	workspace := SessionOrganizationWorkspace{Scope: "project", WorkspaceRoot: root}
	current, err := app.GetSessionOrganization(workspace)
	if err != nil {
		t.Fatal(err)
	}
	ref := func(id string) *SessionSelector {
		return &SessionSelector{Ref: &session.SessionRef{HostID: localDesktopHostID, SessionID: id}}
	}
	moved, err := app.UpdateSessionOrganization(workspace, current.Revision, SessionOrganizationMutation{
		Kind: "move", Target: ref("history-00"), Anchor: ref("history-11"), Position: "before", SortMode: "updated",
	})
	if err != nil || !moved.Applied || !moved.ManualOrderEnabled {
		t.Fatalf("move = %#v, %v", moved, err)
	}
	want := append([]string{"history-00"}, ids[:len(ids)-1]...)
	if got := shown(12); !reflect.DeepEqual(got, want) {
		t.Fatalf("first manual move reordered the rest of the list: %v, want %v", got, want)
	}

	create("fresh")
	if got := shown(5); len(got) == 0 || got[0] != "fresh" {
		t.Fatalf("new session is outside the first sidebar page: %v", got)
	}
}
