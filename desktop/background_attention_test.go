package main

import (
	"context"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/event"
)

func TestDetachedAskPublishesGlobalPendingInteraction(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := writeTopicSessionWithPrompt(t, dir, "a.jsonl", "topic-a", "Conversation A", "", "fixture", time.Now())
	a := NewApp()
	a.ctx = context.Background()
	pending := make(chan RuntimeSessionState, 1)
	a.runtimeEvents.emit = func(_ context.Context, name string, payload ...any) {
		if name != "runtime-state:changed" {
			return
		}
		for _, session := range payload[0].(RuntimeStateProjection).Sessions {
			if !session.Open && len(session.State.Interactions) > 0 {
				select {
				case pending <- session:
				default:
				}
			}
		}
	}
	sink := &tabEventSink{tabID: "A", app: a, ctx: a.ctx}
	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	ctrl := control.New(control.Options{Runner: runner, SessionDir: dir, SessionPath: path, Sink: sink})
	defer ctrl.Close()
	tab := &WorkspaceTab{ID: "A", Scope: "global", TopicID: "topic-a", TopicTitle: "Conversation A", SessionPath: path, Ctrl: ctrl, sink: sink, Ready: true}
	a.tabs[tab.ID] = tab
	a.activeTabID = tab.ID
	ctrl.Submit("fixture turn")
	<-runner.started
	if !a.detachRuntimeForReplacement(tab) {
		t.Fatal("could not detach running A")
	}
	a.mu.Lock()
	delete(a.tabs, "A")
	a.tabs["B"] = &WorkspaceTab{ID: "B"}
	a.activeTabID = "B"
	a.mu.Unlock()
	if sink.context() != nil {
		t.Fatal("detached transcript must stay disconnected")
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ctrl.Ask(ctx, []event.AskQuestion{{ID: "choice", Prompt: "Choose"}})
	}()
	defer func() {
		cancel()
		<-done
		close(runner.release)
		waitNotRunning(t, ctrl)
		a.mu.RLock()
		detached := a.detachedSessions[sessionRuntimeKey(path)]
		a.mu.RUnlock()
		a.quiesceTabAutosave(detached)
	}()
	select {
	case session := <-pending:
		prompt := session.State.Interactions[0]
		if session.TabID == "B" || session.SessionPath != path || prompt.Kind != "ask" || prompt.RequestID == "" || prompt.TurnID == "" {
			t.Fatalf("wrong background prompt identity: %+v", session)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("background Ask was not pushed without reopening A")
	}
	a.mu.RLock()
	active := a.activeTabID
	a.mu.RUnlock()
	if active != "B" {
		t.Fatalf("background Ask changed active tab to %q", active)
	}
}
