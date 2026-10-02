package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/session"
)

// Earlier purge writers dropped the deleted session's topic from every receipt,
// so its retained legacy transcript lists again as a live topic. Clearing that
// row must survive a restart (#10735).
func TestAlreadyRemovedSourceStaysRemovedAfterRestart(t *testing.T) {
	isolateDesktopUserDirs(t)
	path, _, head := migrationSingleDAGFixture(t)
	const topicID = "topic_20260903-142302_04afb4407ec2fa2a"
	if err := agent.UpdateBranchMeta(path, false, func(m *agent.BranchMeta) error {
		m.Scope, m.TopicID, m.TopicTitle = "global", topicID, "Ghost"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := saveProjectsFile(desktopProjectFile{GlobalTopics: []string{topicID}}); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx = t.Context()
	pinDesktopSessionRoot(t, app)
	root := app.desktopSessions.root
	installNoopRuntimeEvents(app)
	t.Cleanup(app.closeSessionServices)
	if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: state.SourceMappings[desktopSourceKey(path, head)].SessionID}
	if err := app.ArchiveCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	if err := app.PurgeCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	app.closeSessionServices()
	dropTopicEvidence(t, app.workspaceRegistry().Path(), ref.SessionID)

	restart := func() *App {
		next := NewApp()
		next.ctx = t.Context()
		next.desktopSessions.root = root
		installNoopRuntimeEvents(next)
		t.Cleanup(next.closeSessionServices)
		installSessionCatalogForTest(t, next, filepath.Dir(path), "global", "")
		t.Cleanup(func() { next.stopSessionCatalog(time.Second) })
		return next
	}
	list := func(a *App) []ProjectNode {
		t.Helper()
		page, err := a.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		a.ReleaseReadSnapshot(page.SnapshotID)
		return page.Items
	}
	app = restart()
	rows := list(app)
	if len(rows) != 1 || rows[0].TopicID != topicID {
		t.Fatalf("fixture must reproduce the residual row: %+v", rows)
	}
	result, err := app.ArchiveSessionTarget(SessionSelector{Source: &SessionSourceRef{HostID: localDesktopHostID, Path: path, HeadID: head}})
	if err != nil || !result.Committed || result.Outcome != "already_removed" {
		t.Fatalf("clear residual row = %+v, %v", result, err)
	}
	app.stopSessionCatalog(time.Second)
	app.closeSessionServices()
	app = restart()
	if rows := list(app); len(rows) != 0 {
		t.Fatalf("cleared residual row returned after restart: %+v", rows)
	}
}

func dropTopicEvidence(t *testing.T, statePath, sessionID string) {
	t.Helper()
	body, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	ops, _ := state["pendingOperations"].(map[string]any)
	for _, raw := range ops {
		op, _ := raw.(map[string]any)
		ids, _ := op["sessionIds"].([]any)
		if slices.Contains(ids, any(sessionID)) {
			delete(op, "presentation")
		}
	}
	if body, err = json.Marshal(state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspacestate.NewStore(statePath).Load(t.Context()); err != nil {
		t.Fatalf("rewritten state: %v", err)
	}
}

func TestRemovedSourceTopicStaysWhileAnotherTranscriptCarriesIt(t *testing.T) {
	dir := t.TempDir()
	const topicID = "topic_shared"
	paths := []string{filepath.Join(dir, "removed.jsonl"), filepath.Join(dir, "sibling.jsonl")}
	for _, path := range paths {
		if err := agent.NewSession("system").Save(path); err != nil {
			t.Fatal(err)
		}
		if err := agent.UpdateBranchMeta(path, false, func(m *agent.BranchMeta) error { m.TopicID = topicID; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	state := workspacestate.State{
		SessionStates:  map[string]workspacestate.SessionState{"gone": {Lifecycle: workspacestate.Deleted}},
		SourceMappings: map[string]workspacestate.SourceMapping{"removed": {SourceKey: "removed", Path: paths[0], SessionID: "gone"}},
	}
	if shared, err := legacyTopicSharedBeyond(state, paths[0], topicID); err != nil || !shared {
		t.Fatalf("unadopted sibling transcript = %v, %v; want shared", shared, err)
	}
	state.SourceMappings["sibling"] = workspacestate.SourceMapping{SourceKey: "sibling", Path: paths[1], SessionID: "gone"}
	if shared, err := legacyTopicSharedBeyond(state, paths[0], topicID); err != nil || shared {
		t.Fatalf("sibling adopted by a deleted session = %v, %v; want unshared", shared, err)
	}
	state.Presentation = map[string]workspacestate.Presentation{"live": {TopicID: topicID}}
	state.SessionStates["live"] = workspacestate.SessionState{Lifecycle: workspacestate.Archived}
	if !topicHasSurvivingOwner(state, topicID) {
		t.Fatal("archived session no longer owns its topic")
	}
}
