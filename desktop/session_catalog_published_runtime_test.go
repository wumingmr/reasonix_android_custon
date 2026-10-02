package main

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

type publishedSidebarController struct {
	bindingRuntimeReader
	published atomic.Pointer[event.RuntimeStateSnapshot]
}

func (c *publishedSidebarController) PublishedRuntimeStateSnapshot() event.RuntimeStateSnapshot {
	return *c.published.Load()
}

func (c *publishedSidebarController) SessionPath() string { return "active-session.jsonl" }

func TestProjectListReadsPublishedStateWhileOtherProjectSamplingBlocks(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		for _, detached := range []bool{false, true} {
			t.Run(testBoolName("catalog", indexed)+"/"+testBoolName("detached", detached), func(t *testing.T) {
				app, dsh, refs := canonicalOrganizationFixture(t, "dsh-history")
				history, ok := app.desktopSessionService("").Runtime(refs["dsh-history"])
				if !ok {
					t.Fatal("history fixture has no runtime")
				}
				appendSessionTestMessage(t, history, "history-user", provider.Message{ID: "history-user", Role: provider.RoleUser, Content: "Existing history"})
				active := t.TempDir()
				if err := addProject(active, "Active project"); err != nil {
					t.Fatal(err)
				}
				if indexed {
					dir := desktopSessionDir(dsh)
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatal(err)
					}
					installSessionCatalogForTest(t, app, dir, "project", dsh)
				}
				ctrl := &publishedSidebarController{bindingRuntimeReader: bindingRuntimeReader{entered: make(chan struct{}), release: make(chan struct{})}}
				release := sync.OnceFunc(func() { close(ctrl.release) })
				defer release()
				ctrl.published.Store(&event.RuntimeStateSnapshot{SchemaVersion: 1, RuntimeEpoch: "active", Revision: 1, Phase: "executing", Running: true})
				tab := &WorkspaceTab{ID: "active", Scope: "project", WorkspaceRoot: active, TopicID: "active-topic", SessionPath: "active-session.jsonl", Ctrl: ctrl}
				if detached {
					app.detachedSessions["active"] = tab
				} else {
					app.tabs["active"] = tab
				}
				// Keep a real refresh call outstanding throughout the list query.
				refreshed := make(chan struct{})
				go func() { ctrl.RuntimeStateSnapshot(); close(refreshed) }()
				<-ctrl.entered
				done := make(chan error, 1)
				go func() {
					defer close(done)
					page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: dsh, Limit: 50})
					if err == nil && (len(page.Items) != 1 || page.Items[0].Session == nil || *page.Items[0].Session != refs["dsh-history"]) {
						err = fmt.Errorf("list lost existing DSH history: %+v", page.Items)
					}
					done <- err
				}()
				defer func() {
					release()
					<-refreshed
					for range done {
					}
				}()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("DSH list waited on the other project's runtime refresh")
				}
				for i, phase := range []string{"executing", "cancelling", "idle"} {
					ctrl.published.Store(&event.RuntimeStateSnapshot{SchemaVersion: 1, RuntimeEpoch: "active", Revision: uint64(i + 1), Phase: phase, Running: phase != "idle", CancelRequested: phase == "cancelling"})
					projection := app.GetRuntimeStateSnapshot()
					if len(projection.Sessions) != 1 || projection.Sessions[0].State.Phase != phase || projection.Sessions[0].Open == detached {
						t.Fatalf("runtime projection lost committed state or binding: %+v", projection)
					}
				}
			})
		}
	}
}

func testBoolName(name string, value bool) string {
	if value {
		return name + "-yes"
	}
	return name + "-no"
}
