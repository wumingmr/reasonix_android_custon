package main

import (
	"sync"
	"testing"
)

func TestTopicActivationTerminalClaimSurvivesPendingPrune(t *testing.T) {
	for _, cancelBuild := range []bool{false, true} {
		name := "direct-tab-switch"
		if cancelBuild {
			name = "topic-switch"
		}
		t.Run(name, func(t *testing.T) {
			app := NewApp()
			app.tabs["old"] = &WorkspaceTab{ID: "old", Ready: true, Ctrl: &activationStubController{}}
			app.activationGen = 1
			app.latestActivationRequestID, app.pendingActivationTabID = "first", "old"
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan bool, 1)
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			app.activationEventHook = func(ev TopicActivationEvent) {
				if ev.RequestID != "first" || ev.Phase != topicActivationPhaseReady {
					t.Errorf("unexpected terminal: %+v", ev)
				}
				close(entered)
				<-release // readiness is published; prune has not started
			}
			go func() { done <- app.emitTopicActivationReadyIfCurrent(1, "first", "old") }()
			<-entered
			app.mu.Lock()
			previous, _ := app.supersedePendingTopicActivationLocked("new", cancelBuild)
			app.latestActivationRequestID, app.pendingActivationTabID = "second", "new"
			app.mu.Unlock()
			if previous != "" {
				t.Errorf("ready activation was cancelled again: %s", previous)
			}
			unblock()
			if !<-done {
				t.Fatal("ready activation did not claim its terminal event")
			}
			app.finishTopicActivation(1, "first")
			if app.latestActivationRequestID != "second" || app.pendingActivationTabID != "new" {
				t.Fatal("late completion cleared the newer navigation")
			}
			app.mu.Lock()
			previous, _ = app.supersedePendingTopicActivationLocked("third", cancelBuild)
			app.mu.Unlock()
			if previous != "second" {
				t.Fatal("unfinished sibling activation lost its cancellation")
			}
			if app.emitTopicActivationReadyIfCurrent(1, "first", "old") {
				t.Fatal("superseded generation emitted readiness")
			}
		})
	}
}
