package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/provider"
)

func awaitRemovalTest[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent removal operation did not complete")
		var zero T
		return zero
	}
}

// The model finishes before removal, but its commit is delayed until removal
// holds the title lock. Removal must already own the removal lock too, and the
// delayed title must not commit after archive (including legacy import).
func TestTopicRemovalConcurrentAIRename(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			a, _, rt, _, path := newCanonicalTitleFixture(t)
			installNoopRuntimeEvents(a)
			topicID := "topic-canonical"
			appendSessionTestMessage(t, rt, "user", provider.Message{ID: "user", Role: provider.RoleUser, Origin: provider.MessageOriginUser, Content: "keep this message"})
			var target SessionTarget
			var err error
			if legacy {
				topic, createErr := a.CreateTopic("global", "", "Legacy")
				if createErr != nil {
					t.Fatal(createErr)
				}
				topicID = topic.ID
				dir := desktopSessionDir(globalWorkspaceRoot())
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(dir, "legacy-title.jsonl")
				writeTopicSessionWithPrompt(t, dir, filepath.Base(path), topicID, "Legacy", globalWorkspaceRoot(), "keep this message", time.Now())
				target = SessionTarget{SessionPath: path, TopicID: topicID}
			} else {
				target, err = a.resolveSessionMutationTarget(SessionSelector{TopicID: topicID})
				if err != nil {
					t.Fatal(err)
				}
			}
			req := inspectedTopicRemoval(t, a, topicID)
			generated, commit := make(chan struct{}), make(chan struct{})
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(commit) }) })
			a.lifecycleCheckpointHook = func(phase string) {
				switch phase {
				case "session-title-before-commit":
					close(generated)
					<-commit
				case "topic-sessions-before-archive":
					if a.sessionRemovalMu.TryLock() {
						a.sessionRemovalMu.Unlock()
						t.Error("removal acquired title before removal ownership")
					}
					release.Do(func() { close(commit) })
				}
			}
			renamed := make(chan error, 1)
			go func() {
				var err error
				if legacy {
					_, err = a.aiRenameLegacySession(t.Context(), target)
				} else {
					_, err = a.aiRenameCanonicalSession(t.Context(), target)
				}
				renamed <- err
			}()
			awaitRemovalTest(t, generated)
			removed := make(chan error, 1)
			go func() {
				out, err := a.RemoveTopic(req)
				if err == nil && !out.Committed {
					err = fmt.Errorf("removal: %+v", out)
				}
				removed <- err
			}()
			if err := awaitRemovalTest(t, removed); err != nil {
				t.Fatal(err)
			}
			if err := awaitRemovalTest(t, renamed); err == nil {
				t.Fatal("stale AI title committed after archive")
			}
			if legacy {
				assertLegacyLifecycle(t, a, path, "archived")
				meta, _, err := agent.LoadBranchMeta(path)
				if err != nil || meta.CustomTitle != "" {
					t.Fatalf("legacy title changed: %+v %v", meta, err)
				}
			} else {
				state, err := a.workspaceRegistry().Load(t.Context())
				if err != nil || state.SessionStates[rt.Ref().SessionID].Lifecycle != workspacestate.Archived {
					t.Fatalf("archive state: %+v %v", state, err)
				}
			}
			if unlock, ok := a.tryLockRuntimeMutation("verify removal completed"); !ok {
				t.Fatal("runtime admission leaked")
			} else {
				unlock()
			}
		})
	}
}

func TestTopicRemovalJoinsAutosaveOutsideTitleLock(t *testing.T) {
	for _, priorSave := range []bool{false, true} {
		t.Run(fmt.Sprintf("prior-save=%t", priorSave), func(t *testing.T) {
			a, _, rt, _, _ := newCanonicalTitleFixture(t)
			installNoopRuntimeEvents(a)
			tab := a.tabs["test"]
			if priorSave {
				a.scheduleTabSnapshot(tab.ID)
				waitForAutosaveIdle(t, tab)
			}
			req := inspectedTopicRemoval(t, a, tab.TopicID)
			titleReached := make(chan struct{})
			a.lifecycleCheckpointHook = func(phase string) {
				switch phase {
				case "before-archive-commit":
					a.scheduleTabSnapshot(tab.ID)
					awaitRemovalTest(t, titleReached)
				case "before-autosave-topic-title":
					close(titleReached)
				}
			}
			done := make(chan error, 1)
			go func() {
				out, err := a.RemoveTopic(req)
				if err == nil && !out.Committed {
					err = fmt.Errorf("removal: %+v", out)
				}
				done <- err
			}()
			if err := awaitRemovalTest(t, done); err != nil {
				t.Fatal(err)
			}
			tab.saveMu.Lock()
			stopped := tab.closing && !tab.saving && tab.saveCond != nil
			tab.saveMu.Unlock()
			if !stopped {
				t.Fatal("removal did not join its autosave")
			}
			a.lifecycleCheckpointHook = nil
			if a.maybeAutoTitleTopic(tab) {
				t.Fatal("removed topic was auto-titled")
			}
			state, err := a.workspaceRegistry().Load(t.Context())
			if err != nil || state.SessionStates[rt.Ref().SessionID].Lifecycle != workspacestate.Archived {
				t.Fatalf("archive state: %+v %v", state, err)
			}
		})
	}
}

func TestPlaceholderRemovalReleasesTitleLocksBeforeCleanup(t *testing.T) {
	a, topic, _ := topicRemovalFixture(t, "global", "Placeholder")
	tab := &WorkspaceTab{ID: "failed", TopicID: topic.ID, Scope: "global", StartupErr: "unavailable", Ready: true}
	a.tabs[tab.ID] = tab
	a.tabOrder = []string{tab.ID}
	a.activeTabID = tab.ID
	checked := false
	a.lifecycleCheckpointHook = func(phase string) {
		if phase != "before-topic-runtime-cleanup" {
			return
		}
		checked = true
		for name, lock := range map[string]*sync.Mutex{"title": &a.topicTitleMutationMu, "index": &topicIndexMu} {
			if !lock.TryLock() {
				t.Errorf("cleanup holds %s lock", name)
			} else {
				lock.Unlock()
			}
		}
		if !a.mu.TryLock() {
			t.Error("cleanup holds App lock")
		} else {
			a.mu.Unlock()
		}
		if a.sessionRemovalMu.TryLock() {
			a.sessionRemovalMu.Unlock()
			t.Error("cleanup lost removal ownership")
		}
	}
	out, err := a.RemoveTopic(inspectedTopicRemoval(t, a, topic.ID))
	if err != nil || !out.Committed || !checked {
		t.Fatalf("removal: %+v %v cleanup=%t", out, err, checked)
	}
}

type removalBlockingSnapshot struct {
	control.SessionAPI
	started chan struct{}
	resume  chan struct{}
}

func (c *removalBlockingSnapshot) Snapshot() error {
	close(c.started)
	<-c.resume
	return c.SessionAPI.Snapshot()
}

func TestQuiesceWaitsForFirstAutosave(t *testing.T) {
	isolateDesktopUserDirs(t)
	a, tab := appWithTab(t, filepath.Join(t.TempDir(), "first.jsonl"))
	c := &removalBlockingSnapshot{SessionAPI: tab.Ctrl, started: make(chan struct{}), resume: make(chan struct{})}
	tab.Ctrl = c
	var release sync.Once
	defer release.Do(func() { close(c.resume) })
	a.scheduleTabSnapshot(tab.ID)
	awaitRemovalTest(t, c.started)
	tab.saveMu.Lock()
	initialized := tab.saving && tab.saveCond != nil
	tab.saveMu.Unlock()
	if !initialized {
		t.Fatal("first in-flight save has no completion condition")
	}
	done := make(chan struct{})
	go func() { a.quiesceTabAutosave(tab); close(done) }()
	// Acquiring saveMu after closing is set proves the join has reached its
	// condition wait while the actual Snapshot is still blocked.
	deadline := time.Now().Add(10 * time.Second)
	for {
		tab.saveMu.Lock()
		closing := tab.closing
		tab.saveMu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("quiesce did not stop save admission")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("quiesce returned before the first write finished")
	default:
	}
	release.Do(func() { close(c.resume) })
	awaitRemovalTest(t, done)
	if tab.saving {
		t.Fatal("save still running after join")
	}
}
