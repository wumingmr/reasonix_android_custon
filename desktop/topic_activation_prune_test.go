package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestTopicActivationPendingPruneDoesNotBlockNextHistoryOpen(t *testing.T) {
	app, source, target, root, _ := canonicalWorkspaceOpenFixture(t)
	app.readyHook = func() {}
	installNoopRuntimeEvents(app, source.sink)
	events := newActivationEventRecorder(app)
	t.Cleanup(func() { app.shutdown(context.Background()) })
	pruning := make(chan struct{}, 4)
	app.runtimeMutationBeforeLockHook = func(operation string) {
		if operation == "prune-visible-tabs" {
			pruning <- struct{}{}
		}
	}
	app.runtimeRebuildMu.Lock()
	unlock := sync.OnceFunc(app.runtimeRebuildMu.Unlock)
	defer unlock()
	_, err := app.StartTopicActivation(TopicActivationRequest{
		Scope: source.Scope, WorkspaceRoot: source.WorkspaceRoot,
		SessionPath: sessionRoute(source.SessionID), RequestID: "source",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-pruning:
	case <-time.After(5 * time.Second):
		t.Fatal("source completion did not reach runtime pruning")
	}
	type result struct {
		ticket TopicActivationTicket
		err    error
	}
	done := make(chan result, 1)
	go func() {
		ticket, err := app.StartTopicActivation(TopicActivationRequest{
			Scope: "project", WorkspaceRoot: root, SessionPath: sessionRoute(target.Ref().SessionID), RequestID: "target",
		})
		done <- result{ticket, err}
	}()
	var ticket TopicActivationTicket
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		ticket = got.ticket
	case <-time.After(5 * time.Second):
		unlock()
		<-done
		t.Fatal("previous activation's runtime prune blocked the next history identity")
	}
	canonicalTopicHistoryReady(t, app, ticket.TabID)
	unlock()
	events.waitFor(t, activationEventFor("target", "ready"))
	app.mu.RLock()
	visible := app.tabs[app.activeTabID]
	correct := visible != nil && visible.ID == ticket.TabID && visible.SessionID == target.Ref().SessionID
	app.mu.RUnlock()
	if !correct {
		t.Fatal("late prune replaced the latest selected history")
	}
}
