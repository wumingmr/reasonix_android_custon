package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"reasonix/desktop/internal/workspacestate"
)

func TestResetManualSessionOrderKeepsSavedOrderAndGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace-state-v1.json")
	store := workspacestate.NewStore(path)
	ctx := t.Context()
	id := workspacestate.GlobalWorkspaceID
	if err := store.EnsureWorkspace(ctx, workspacestate.Workspace{ID: id}); err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range []string{"older", "newer"} {
		if err := store.AttachSession(ctx, "", id, sessionID, ""); err != nil {
			t.Fatal(err)
		}
	}
	manual, _, err := store.UpdateOrganization(ctx, id, nil, func(o *workspacestate.Organization) error {
		o.ManualOrderEnabled = true
		o.Order = []string{workspacestate.SessionKey("older"), workspacestate.SessionKey("newer")}
		o.Groups = []workspacestate.OrganizationGroup{{ID: "work", Title: "Work", Members: []string{workspacestate.SessionKey("older")}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	older := ProjectNode{Key: "older", TopicID: "older", CreatedAt: 1, LastActivityAt: 1, SortOrder: 0}
	newer := ProjectNode{Key: "newer", TopicID: "newer", CreatedAt: 2, LastActivityAt: 2, SortOrder: 1}
	if !projectTopicLess(older, newer, "updated", manual.ManualOrderEnabled) {
		t.Fatal("fixture did not reproduce manual order overriding recent activity")
	}
	if _, applied, err := store.UpdateOrganization(ctx, id, &manual.Revision, func(o *workspacestate.Organization) error {
		return applyOrganizationMutation(o, SessionOrganizationMutation{Kind: "reset-order"}, "", "")
	}); err != nil || !applied {
		t.Fatalf("reset manual order: applied=%v err=%v", applied, err)
	}
	state, err := workspacestate.NewStore(path).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := state.Workspaces[id].Organization
	if got.ManualOrderEnabled {
		t.Fatal("manual ordering remained enabled after reset")
	}
	if !projectTopicLess(newer, older, "updated", got.ManualOrderEnabled) || !projectTopicLess(newer, older, "created", got.ManualOrderEnabled) {
		t.Fatal("time sorting did not resume after reset")
	}
	if !reflect.DeepEqual(got.Order, manual.Order) || len(got.Groups) != 1 || got.Groups[0].ID != "work" ||
		!reflect.DeepEqual(got.Groups[0].Members, manual.Groups[0].Members) {
		t.Fatalf("reset changed saved order or groups: got=%+v before=%+v", got, manual)
	}
}
