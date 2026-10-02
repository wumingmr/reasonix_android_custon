package workspacestate

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOrganizationLegacyMoveUpdatesAuthoritativeOrder(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "registry.json"))
	if err := store.EnsureWorkspace(t.Context(), Workspace{ID: GlobalWorkspaceID}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := store.AttachSession(t.Context(), "", GlobalWorkspaceID, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := store.UpdateOrganization(t.Context(), GlobalWorkspaceID, nil, func(o *Organization) error {
		o.Order = []string{SessionKey("a"), SessionKey("b")}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MoveSession(t.Context(), GlobalWorkspaceID, "b", "a"); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	o := state.Workspaces[GlobalWorkspaceID].Organization
	if !o.ManualOrderEnabled || !reflect.DeepEqual(o.Order, []string{SessionKey("b"), SessionKey("a")}) {
		t.Fatalf("organization did not follow move: %+v", o)
	}
}

func TestOrganizationV2UpgradeBacksUpOriginalAndPreservesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	original := []byte(`{"version":2,"generation":7,"workspaceIds":["global"],"workspaces":{"global":{"id":"global","title":"Global","sessionIds":[],"futureWorkspace":{"enabled":true},"organization":{"revision":3,"manualOrderEnabled":false,"order":[],"groups":[{"id":"group","title":"Work","members":[],"futureGroup":[1,2]}],"migrationVersion":1,"imported":{},"futureOrganization":{"keep":"yes"}}}},"sessionStates":{},"futureRoot":{"value":42}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	if _, err := store.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, original) {
		t.Fatalf("read-only load changed v2 source: body=%s err=%v", before, err)
	}
	revision := uint64(3)
	if _, applied, err := store.UpdateOrganization(t.Context(), GlobalWorkspaceID, &revision, func(o *Organization) error {
		o.Groups[0].Title = "Renamed"
		return nil
	}); err != nil || !applied {
		t.Fatalf("upgrade organization: applied=%v err=%v", applied, err)
	}
	backup, err := os.ReadFile(path + ".v2.bak")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("v2 backup differs from original: body=%s err=%v", backup, err)
	}
	state, err := NewStore(path).Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 3 || state.Workspaces[GlobalWorkspaceID].Organization.Revision != 4 {
		t.Fatalf("upgraded state: %#v", state)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	workspace := document["workspaces"].(map[string]any)[GlobalWorkspaceID].(map[string]any)
	organization := workspace["organization"].(map[string]any)
	group := organization["groups"].([]any)[0].(map[string]any)
	for name, pair := range map[string][2]any{
		"root":         {document["futureRoot"], map[string]any{"value": float64(42)}},
		"workspace":    {workspace["futureWorkspace"], map[string]any{"enabled": true}},
		"organization": {organization["futureOrganization"], map[string]any{"keep": "yes"}},
		"group":        {group["futureGroup"], []any{float64(1), float64(2)}},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("unknown %s field = %#v, want %#v", name, pair[0], pair[1])
		}
	}
	if err := store.RenameWorkspace(t.Context(), GlobalWorkspaceID, "Changed again"); err != nil {
		t.Fatal(err)
	}
	backup, err = os.ReadFile(path + ".v2.bak")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatal("later writes replaced the original v2 backup")
	}
}

func TestOrganizationCASRejectsStaleWriterWithoutOverwritingWinner(t *testing.T) {
	store := organizationTestStore(t)
	revision := uint64(0)
	winner, applied, err := store.UpdateOrganization(t.Context(), GlobalWorkspaceID, &revision, func(o *Organization) error {
		o.Groups = []OrganizationGroup{{ID: "winner", Title: "Other window", Members: []string{}}}
		return nil
	})
	if err != nil || !applied {
		t.Fatalf("first writer: applied=%v err=%v", applied, err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	invoked := false
	latest, applied, err := NewStore(store.Path()).UpdateOrganization(t.Context(), GlobalWorkspaceID, &revision, func(o *Organization) error {
		invoked = true
		o.Groups = []OrganizationGroup{{ID: "loser", Title: "Stale window"}}
		return nil
	})
	if err != nil || applied || invoked {
		t.Fatalf("stale writer: applied=%v invoked=%v err=%v", applied, invoked, err)
	}
	latestJSON, _ := json.Marshal(latest)
	winnerJSON, _ := json.Marshal(winner)
	if !bytes.Equal(latestJSON, winnerJSON) {
		t.Fatalf("conflict snapshot=%#v, want winner=%#v", latest, winner)
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("conflicting organization update changed the registry")
	}
}

func TestOrganizationSourceJournalCommitPreservesUngroupedPlacement(t *testing.T) {
	store := organizationTestStore(t)
	ctx := t.Context()
	if err := store.AttachSession(ctx, "", GlobalWorkspaceID, "sibling", ""); err != nil {
		t.Fatal(err)
	}
	sourceKey := "source\x00local\x00legacy-b"
	_, _, err := store.UpdateOrganization(ctx, GlobalWorkspaceID, nil, func(o *Organization) error {
		o.ManualOrderEnabled = true
		o.Order = []string{sourceKey, SessionKey("sibling")}
		o.Groups = []OrganizationGroup{{ID: "shared-topic-group", Title: "Shared topic", Members: []string{SessionKey("sibling")}}}
		o.MigrationVersion = 1
		o.Imported = map[string]bool{sourceKey: true, SessionKey("sibling"): true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mapping := &SourceMapping{SourceKey: "legacy-b", Path: "/legacy/b.jsonl", Format: "legacy", Fingerprint: "fingerprint", SessionID: "b", WorkspaceID: GlobalWorkspaceID}
	if err := store.BeginOperation(ctx, Operation{ID: "adopt-b", Kind: "import", WorkspaceID: GlobalWorkspaceID, Lifecycle: Active}); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareOperationContent(ctx, "adopt-b", []string{"b"}, mapping, &Presentation{TopicID: "shared-topic"}); err != nil {
		t.Fatal(err)
	}
	// Reopen between prepare and commit, as migration recovery does after a crash.
	store = NewStore(store.Path())
	if err := store.CommitOperation(ctx, "adopt-b"); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	o := state.Workspaces[GlobalWorkspaceID].Organization
	assertStrings(t, o.Order, []string{SessionKey("b"), SessionKey("sibling")})
	assertStrings(t, o.Groups[0].Members, []string{SessionKey("sibling")})
	assertStrings(t, state.Workspaces[GlobalWorkspaceID].SessionIDs, []string{"b", "sibling"})
	if o.Imported[sourceKey] || !o.Imported[SessionKey("b")] {
		t.Fatalf("source import identity was not transferred: %#v", o.Imported)
	}
	if state.SourceMappings["legacy-b"].SessionID != "b" {
		t.Fatal("organization committed without its source mapping")
	}
	revision := o.Revision
	if err := store.CommitOperation(ctx, "adopt-b"); err != nil {
		t.Fatal(err)
	}
	state, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Workspaces[GlobalWorkspaceID].Organization.Revision != revision {
		t.Fatal("replaying a committed adoption changed organization again")
	}
}

func TestOrganizationForkAttachmentInheritsGroupAndFollowsParent(t *testing.T) {
	store := organizationTestStore(t)
	ctx := t.Context()
	for _, id := range []string{"parent", "sibling"} {
		if err := store.AttachSession(ctx, "", GlobalWorkspaceID, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := store.UpdateOrganization(ctx, GlobalWorkspaceID, nil, func(o *Organization) error {
		o.ManualOrderEnabled = true
		o.Order = []string{SessionKey("parent"), SessionKey("sibling")}
		o.Groups = []OrganizationGroup{{ID: "feature", Title: "Feature", Members: []string{SessionKey("parent")}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parentGeneration := state.SessionStates["parent"].Generation
	if err := store.BeginCreate(ctx, PendingCreate{OperationID: "fork", WorkspaceID: GlobalWorkspaceID, SessionID: "child"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSessionFromSourceIfUnchanged(ctx, "fork", GlobalWorkspaceID, "child", "sibling", "parent", parentGeneration); err != nil {
		t.Fatal(err)
	}
	state, err = NewStore(store.Path()).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workspace := state.Workspaces[GlobalWorkspaceID]
	assertStrings(t, workspace.Organization.Order, []string{SessionKey("parent"), SessionKey("child"), SessionKey("sibling")})
	assertStrings(t, workspace.Organization.Groups[0].Members, []string{SessionKey("parent"), SessionKey("child")})
	assertStrings(t, workspace.SessionIDs, []string{"parent", "child", "sibling"})
	revision := workspace.Organization.Revision
	if err := store.AttachSessionFromSourceIfUnchanged(ctx, "fork", GlobalWorkspaceID, "child", "sibling", "parent", parentGeneration); err != nil {
		t.Fatal(err)
	}
	state, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Workspaces[GlobalWorkspaceID].Organization.Revision != revision {
		t.Fatal("fork retry changed organization or duplicated its child")
	}
}

func TestOrganizationFailedMutationLeavesOrderAndRevisionUnchanged(t *testing.T) {
	store := organizationTestStore(t)
	ctx := t.Context()
	if _, _, err := store.UpdateOrganization(ctx, GlobalWorkspaceID, nil, func(o *Organization) error {
		o.Order = []string{"source\x00local\x00before"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("failed validation")
	if _, applied, err := store.UpdateOrganization(ctx, GlobalWorkspaceID, nil, func(o *Organization) error {
		o.Order = []string{"source\x00local\x00after"}
		return failure
	}); !errors.Is(err, failure) || applied {
		t.Fatalf("failed mutation: applied=%v err=%v", applied, err)
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed callback partially published organization")
	}
}

func TestOrganizationAdoptingOneHeadPreservesOtherHeadAtSamePath(t *testing.T) {
	store := organizationTestStore(t)
	ctx := t.Context()
	first := "source\x00local\x00path:head-a"
	second := "source\x00local\x00path:head-b"
	if _, _, err := store.UpdateOrganization(ctx, GlobalWorkspaceID, nil, func(o *Organization) error {
		o.ManualOrderEnabled = true
		o.Order = []string{first, second}
		o.Groups = []OrganizationGroup{{ID: "heads", Members: []string{first, second}}}
		o.Imported = map[string]bool{first: true, second: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSource(ctx, SourceMapping{
		SourceKey: "path:head-a", Path: "/legacy/shared.jsonl", HeadID: "head-a",
		SessionID: "a", WorkspaceID: GlobalWorkspaceID, Fingerprint: "same-file", Format: "legacy",
	}, Presentation{}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	o := state.Workspaces[GlobalWorkspaceID].Organization
	assertStrings(t, o.Order, []string{SessionKey("a"), second})
	assertStrings(t, o.Groups[0].Members, []string{SessionKey("a"), second})
	if !o.Imported[second] || !o.Imported[SessionKey("a")] || o.Imported[first] {
		t.Fatalf("head adoption import identities: %#v", o.Imported)
	}
}

func TestOrganizationAdoptionPreservesExistingCanonicalUserSettings(t *testing.T) {
	store := organizationTestStore(t)
	ctx := t.Context()
	source := "source\x00local\x00legacy"
	canonical := SessionKey("a")
	if _, _, err := store.UpdateOrganization(ctx, GlobalWorkspaceID, nil, func(o *Organization) error {
		o.ManualOrderEnabled = true
		o.Order = []string{source, SessionKey("other"), canonical}
		o.Groups = []OrganizationGroup{{ID: "old-topic", Members: []string{source}}}
		o.Imported = map[string]bool{source: true, canonical: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSource(ctx, SourceMapping{
		SourceKey: "legacy", Path: "/legacy/a.jsonl", SessionID: "a",
		WorkspaceID: GlobalWorkspaceID, Fingerprint: "fingerprint", Format: "legacy",
	}, Presentation{}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	o := state.Workspaces[GlobalWorkspaceID].Organization
	assertStrings(t, o.Order, []string{SessionKey("other"), canonical})
	assertStrings(t, o.Groups[0].Members, []string{})
	if !o.Imported[canonical] || o.Imported[source] {
		t.Fatalf("canonical settings import identities: %#v", o.Imported)
	}
}

func TestOrganizationForkPreservesActivitySortWithoutEnablingManualOrder(t *testing.T) {
	store := organizationTestStore(t)
	ctx := t.Context()
	if err := store.AttachSession(ctx, "", GlobalWorkspaceID, "parent", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.UpdateOrganization(ctx, GlobalWorkspaceID, nil, func(o *Organization) error {
		o.ManualOrderEnabled = false
		o.Order = []string{SessionKey("parent")}
		o.Groups = []OrganizationGroup{{ID: "feature", Title: "Feature", Members: []string{SessionKey("parent")}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginCreate(ctx, PendingCreate{OperationID: "fork-activity", WorkspaceID: GlobalWorkspaceID, SessionID: "child"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSessionFromSourceIfUnchanged(ctx, "fork-activity", GlobalWorkspaceID, "child", "", "parent", state.SessionStates["parent"].Generation); err != nil {
		t.Fatal(err)
	}
	state, err = NewStore(store.Path()).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	o := state.Workspaces[GlobalWorkspaceID].Organization
	if o.ManualOrderEnabled {
		t.Fatal("forking implicitly changed activity sorting to manual order")
	}
	assertStrings(t, o.Groups[0].Members, []string{SessionKey("parent"), SessionKey("child")})
}

func TestManualOrderPlacesNewSessionAtTheHead(t *testing.T) {
	store := organizationTestStore(t)
	ctx := t.Context()
	if _, _, err := store.UpdateOrganization(ctx, GlobalWorkspaceID, nil, func(*Organization) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"older", "newer"} {
		if err := store.AttachSession(ctx, "", GlobalWorkspaceID, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MoveSession(ctx, GlobalWorkspaceID, "newer", "older"); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSession(ctx, "", GlobalWorkspaceID, "fresh", ""); err != nil {
		t.Fatal(err)
	}
	state, err := NewStore(store.Path()).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	o := state.Workspaces[GlobalWorkspaceID].Organization
	assertStrings(t, o.Order, []string{SessionKey("fresh"), SessionKey("newer"), SessionKey("older")})
	assertStrings(t, state.Workspaces[GlobalWorkspaceID].SessionIDs, []string{"fresh", "newer", "older"})
}

func organizationTestStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), "registry.json"))
	if err := store.EnsureWorkspace(t.Context(), Workspace{ID: GlobalWorkspaceID, Visible: true}); err != nil {
		t.Fatal(err)
	}
	return store
}
