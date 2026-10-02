package control

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/tool"
	"reasonix/internal/transcript"
)

func TestNativeResumePrefersCompletedCanonicalCutover(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "old.jsonl")
	if err := os.WriteFile(path, []byte("{\"role\":\"user\",\"content\":\"stale\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := session.NewService("local", session.NewFilesystemPersistence(filepath.Join(root, "canonical")))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: agent.BranchID(path)})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "newer", Role: provider.RoleUser, Content: "new canonical work"}})
	if _, err := runtime.Session().Append(t.Context(), session.Batch{OperationID: "newer", Events: []session.Event{{Kind: "message/complete", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession(""), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, NativeLegacySession: true})
	loaded, err := agent.LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ResumeNativeSession(loaded, path); err != nil {
		t.Fatal(err)
	}
	if !c.UsesExclusiveSession() || len(c.History()) != 1 || c.History()[0].Content != "new canonical work" {
		t.Fatalf("resumed stale source: %+v", c.History())
	}
}

func TestNativeLegacyResumeContinueAndNew(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "old.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\"role\":\"system\",\"content\":\"system\"}\n{\"role\":\"user\",\"content\":\"old question\"}\n{\"role\":\"assistant\",\"content\":\"old answer\"}\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := agent.LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	service, err := session.NewService("local", session.NewFilesystemPersistence(filepath.Join(root, "canonical")))
	if err != nil {
		t.Fatal(err)
	}
	exec := agent.New(testutil.NewMock("test", testutil.Turn{Text: "continued answer"}), tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Runner: exec, Executor: exec, Sink: event.Discard, SystemPrompt: "system", SessionDir: filepath.Dir(path), SessionService: service, NativeLegacySession: true})
	c.Resume(loaded, path)
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("resume rewrote source: %v", err)
	}
	if _, err := os.Stat(sessionDirectory(path)); !os.IsNotExist(err) {
		t.Fatalf("resume created canonical copy: %v", err)
	}
	follow, err := c.TranscriptFollow(t.Context(), transcript.FollowRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RunTurn(t.Context(), "continue"); err != nil {
		t.Fatal(err)
	}
	if err := c.Snapshot(); err != nil {
		t.Fatal(err)
	}
	next, err := c.TranscriptFollow(t.Context(), transcript.FollowRequest{Subscription: follow.Subscription, AfterRevision: follow.Snapshot.ProjectionRevision})
	if err != nil || len(next.Changes) == 0 {
		t.Fatalf("legacy follow lost live changes: %+v %v", next, err)
	}
	_, _ = c.TranscriptFollow(context.Background(), transcript.FollowRequest{Subscription: follow.Subscription, Close: true})
	reopened, err := agent.LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	messages := reopened.Snapshot()
	if last := messages[len(messages)-1]; last.Role != provider.RoleAssistant || last.Content != "continued answer" {
		t.Fatalf("continued message missing: %+v", messages)
	}
	if _, err := os.Stat(sessionDirectory(path)); !os.IsNotExist(err) {
		t.Fatalf("continuation created canonical copy: %v", err)
	}
	if err := c.NewSession(); err != nil {
		t.Fatal(err)
	}
	if !c.UsesExclusiveSession() || c.NativeLegacySession() {
		t.Fatal("new session retained legacy backend")
	}
	if _, bound := c.SessionRef(); !bound {
		t.Fatal("new session has no canonical identity")
	}
}

func TestNativeDAGHeadIsNotReplacedByAnotherHeadsStore(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "branches.jsonl")
	loaded := agent.NewSession("system")
	loaded.Add(provider.Message{Role: provider.RoleUser, Content: "shared"})
	if err := loaded.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.ForkHead(path, loaded.Snapshot()[1].ID, agent.HeadKindFork, "alternate"); err != nil {
		t.Fatal(err)
	}
	loaded.Add(provider.Message{Role: provider.RoleAssistant, Content: "alternate answer"})
	if err := loaded.Save(path); err != nil {
		t.Fatal(err)
	}
	service, err := session.NewService("local", session.NewFilesystemPersistence(filepath.Join(root, "canonical")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(t.Context(), session.CreateOptions{SessionID: agent.BranchID(path)}); err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession(""), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{Executor: exec, Sink: event.Discard, SessionService: service, NativeLegacySession: true})
	if err := c.ResumeNativeSession(loaded, path); err != nil {
		t.Fatal(err)
	}
	if !c.NativeLegacySession() || c.History()[2].Content != "alternate answer" {
		t.Fatal("a path alias replaced the requested DAG head")
	}
}
