package main

import (
	"os"
	"path/filepath"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"strings"
	"testing"
	"time"
)

func attachReviewTitleTarget(t *testing.T, app *App, runtime *session.Runtime, topic string) {
	t.Helper()
	workspace, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().AttachSession(t.Context(), "", workspace, runtime.Ref().SessionID, ""); err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().EnsureSessionTopic(t.Context(), runtime.Ref().SessionID, topic, ""); err != nil {
		t.Fatal(err)
	}
	appendSessionTestMessage(t, runtime, "probe-user", provider.Message{ID: "probe-user", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: "durable target content"})
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAIRenameColdSessionWithoutAnyController(t *testing.T) {
	app, _, runtime, _, _ := newCanonicalTitleFixture(t)
	attachReviewTitleTarget(t, app, runtime, "cold-target")
	app.tabs = map[string]*WorkspaceTab{}
	app.activeTabID = ""
	_, err := app.AIRenameSession(sessionRoute(runtime.Ref().SessionID))
	if err != nil {
		t.Fatalf("durable target still requires a controller: %v", err)
	}
}

func TestSessionTargetExplicitRefMustNotMatchSiblingTopicController(t *testing.T) {
	app, _, original, _, _ := newCanonicalTitleFixture(t)
	attachReviewTitleTarget(t, app, original, "topic-canonical")
	sibling, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: "sibling-review"})
	if err != nil {
		t.Fatal(err)
	}
	attachReviewTitleTarget(t, app, sibling, "topic-canonical")
	ref := sibling.Ref()
	target, err := app.resolveSessionTarget(sessionTargetSelector{Ref: &ref, TopicID: "topic-canonical"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Controller != nil {
		actual, _ := target.Controller.SessionRef()
		if actual != ref {
			t.Fatalf("explicit ref %s attached sibling controller %s", ref.SessionID, actual.SessionID)
		}
	}
	if _, err := app.AIRenameSession("topic-canonical"); err == nil {
		t.Fatal("ambiguous topic-only rename must not choose an arbitrary session")
	}
	if _, err := app.AIRenameSession(sessionRoute(ref.SessionID)); err != nil {
		t.Fatal(err)
	}
	info, err := app.desktopSessionService("").Query().Stat(t.Context(), original.Ref())
	if err != nil || info.Title != "" {
		t.Fatalf("renaming explicit sibling changed the original: %+v, %v", info, err)
	}
}

func TestSessionTargetInvalidHighPriorityRefDoesNotFallBack(t *testing.T) {
	app, _, _, _, path := newCanonicalTitleFixture(t)
	target, err := app.resolveSessionTarget(SessionSelector{
		Ref:         &session.SessionRef{HostID: localDesktopHostID},
		SessionPath: path,
	})
	if err == nil || !strings.Contains(err.Error(), "session_operation:target_not_found:") {
		t.Fatalf("invalid explicit ref resolved target %+v with err %v", target, err)
	}
}

func TestSessionTargetRemoteRefDoesNotEnterLocalResolver(t *testing.T) {
	app := NewApp()
	ref := session.SessionRef{HostID: "remote-host", SessionID: "remote-session"}
	_, err := app.resolveSessionTarget(SessionSelector{Ref: &ref, SessionPath: "/must/not/fallback.jsonl", TopicID: "fallback"})
	if err == nil || !strings.Contains(err.Error(), "session_operation:unsupported:") {
		t.Fatalf("remote target error = %v, want structured unsupported", err)
	}
}

func TestSessionTargetTopicRejectsMultipleLegacySessions(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		path := writeTargetHistoryFixture(t, dir, name, name)
		if err := agent.UpdateBranchMeta(path, false, func(meta *agent.BranchMeta) error {
			meta.TopicID = "shared-legacy-topic"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	app := NewApp()
	installSessionCatalogForTest(t, app, dir, "global", "")
	target, err := app.resolveSessionTarget(SessionSelector{TopicID: "shared-legacy-topic"})
	if err == nil || !strings.Contains(err.Error(), "session_operation:ambiguous_target:") {
		t.Fatalf("ambiguous legacy topic resolved target %+v with err %v", target, err)
	}
}

func TestAIRenameArchiveDuringColdRenameRejectsCompletion(t *testing.T) {
	app, _, _, prov, path := newCanonicalTitleFixture(t)
	runtime, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: "archive-target", CWD: globalWorkspaceRoot()})
	if err != nil {
		t.Fatal(err)
	}
	attachReviewTitleTarget(t, app, runtime, "cold-target")
	generator := newDesktopSessionTitleController(filepath.Dir(path), path, prov)
	t.Cleanup(generator.Close)
	app.tabs["test"].Ctrl = generator
	app.tabs["test"].TopicID = "other-topic"
	app.tabs["test"].SessionID = "other-session"
	prov.started = make(chan struct{})
	prov.chunks = make(chan provider.Chunk, 2)
	result := make(chan error, 1)
	go func() { _, err := app.AIRenameSession("cold-target"); result <- err }()
	select {
	case <-prov.started:
	case err := <-result:
		t.Fatalf("rename stopped early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	if err := app.ArchiveCanonicalSession(runtime.Ref()); err != nil {
		t.Fatal(err)
	}
	prov.chunks <- provider.Chunk{Type: provider.ChunkText, Text: "stale renamed archive"}
	prov.chunks <- provider.Chunk{Type: provider.ChunkDone}
	close(prov.chunks)
	if err := <-result; err == nil {
		t.Fatal("AI rename succeeded after target was archived")
	}
}
