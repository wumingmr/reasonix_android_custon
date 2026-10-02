package main

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

func TestTopicActivationReselectsCancelledStartup(t *testing.T) {
	for _, canonical := range []bool{true, false} {
		name := "legacy"
		if canonical {
			name = "canonical"
		}
		t.Run(name, func(t *testing.T) {
			app, source, target, root, _ := canonicalWorkspaceOpenFixture(t)
			app.readyHook = func() {}
			installNoopRuntimeEvents(app, source.sink)
			t.Cleanup(func() { app.shutdown(context.Background()) })
			path := sessionRoute(target.Ref().SessionID)
			if !canonical {
				saveWorkspace(root)
				app.registerProjectRoot(root)
				dir := desktopSessionDir(root)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				path = writeTopicSession(t, dir, "reselect.jsonl", "reselect-topic", "Reselect", root)
			}

			buildEntered := make(chan struct{}, 4)
			releaseBuilds := make(chan struct{})
			unblockBuilds := sync.OnceFunc(func() { close(releaseBuilds) })
			defer unblockBuilds()
			app.tabBuildStartHook = func(string) { buildEntered <- struct{}{}; <-releaseBuilds }
			events := newActivationEventRecorder(app)
			releaseCompletion := make(chan struct{})
			unblockCompletion := sync.OnceFunc(func() { close(releaseCompletion) })
			defer unblockCompletion()
			// Keep the cancelled target present until the return click. This is
			// the window between a ready event and the previous surface's prune.
			app.activationEventHook = func(ev TopicActivationEvent) {
				events.ch <- ev
				if ev.RequestID == "away" && ev.Phase == "ready" {
					<-releaseCompletion
				}
			}
			request := TopicActivationRequest{Scope: "project", WorkspaceRoot: root, TopicID: "reselect-topic", SessionPath: path, RequestID: "first"}
			first, err := app.StartTopicActivation(request)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-buildEntered:
			case <-time.After(5 * time.Second):
				t.Fatal("target startup did not reach the build gate")
			}
			_, err = app.StartTopicActivation(TopicActivationRequest{
				Scope: source.Scope, WorkspaceRoot: source.WorkspaceRoot,
				SessionPath: sessionRoute(source.SessionID), RequestID: "away",
			})
			if err != nil {
				t.Fatal(err)
			}
			events.waitFor(t, activationEventFor("away", "ready"))
			request.RequestID = "return"
			last, err := app.StartTopicActivation(request)
			if err != nil {
				t.Fatal(err)
			}
			unblockBuilds()
			unblockCompletion()
			terminal := events.waitFor(t, func(ev TopicActivationEvent) bool {
				return ev.RequestID == "return" && (ev.Phase == "ready" || ev.Phase == "failed")
			})
			if terminal.Phase != "ready" {
				t.Fatalf("return click reused an abandoned startup: first=%s last=%s event=%+v", first.TabID, last.TabID, terminal)
			}
			app.mu.RLock()
			visible := app.tabs[app.activeTabID]
			correct := visible != nil && visible.ID == last.TabID && visible.Ctrl != nil
			if canonical {
				correct = correct && visible.SessionID == target.Ref().SessionID
			}
			app.mu.RUnlock()
			if !correct {
				t.Fatal("reselected startup did not publish the requested session")
			}
		})
	}
}
