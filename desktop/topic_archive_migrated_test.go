package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
)

func TestMigratedTopicArchiveOwnershipRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	tab := &WorkspaceTab{ID: "migrated", SessionID: "canonical-session"}
	if err := tab.ensureSessionLease(path); err != nil {
		t.Fatal(err)
	}
	defer tab.releaseSessionLease()
	targets := []topicTrashTarget{{dir: filepath.Dir(path), sessionPath: path, key: filepath.Base(path)}}
	batch, err := acquireTopicArchiveOwnership(targets, []removedSessionRuntime{{tab: tab}})
	if err != nil {
		t.Fatal(err)
	}
	defer batch.rollback()
	assertOwned := func() {
		t.Helper()
		lease, err := agent.TryAcquireSessionLease(path)
		if lease != nil {
			lease.Release()
		}
		if !errors.Is(err, agent.ErrSessionLeaseHeld) {
			t.Fatalf("competing writer acquired the legacy lease: %v", err)
		}
	}
	assertOwned()
	batch.rollback()
	if tab.sessionLeaseRuntimeKey() != sessionRuntimeKey(path) || tab.SessionID != "canonical-session" || tab.SessionPath != "" {
		t.Fatal("rollback did not preserve canonical identity and legacy ownership")
	}
	assertOwned()
}

func TestArchiveWithMigratedLeaseOwner(t *testing.T) {
	for _, operation := range []string{"topic", "session"} {
		t.Run(operation, func(t *testing.T) {
			for _, detached := range []bool{false, true} {
				name := "visible"
				if detached {
					name = "detached"
				}
				t.Run(name, func(t *testing.T) {
					isolateDesktopUserDirs(t)
					root := canonicalRuntimeRoot(t.TempDir())
					topicID := "topic_migrated_lease"
					if err := addProject(root, ""); err != nil {
						t.Fatal(err)
					}
					if err := setTopicTitle(root, topicID, "Migrated session"); err != nil {
						t.Fatal(err)
					}
					dir := config.SessionDir()
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatal(err)
					}
					path := writeTopicSessionWithPrompt(t, dir, "migrated.jsonl", topicID, "Migrated session", root, "keep history", time.Now())
					// Canonical identity publication clears the legacy path while the
					// tab can still own the imported file's compatibility lease.
					app := NewApp()
					pinDesktopSessionRoot(t, app)
					ctrl := control.New(control.Options{
						SessionDir: dir, WorkspaceRoot: root,
						Executor:       agent.New(nil, nil, agent.NewSession("test"), agent.Options{}, event.Discard),
						SessionService: app.desktopSessionService(dir), ExclusiveSession: true,
					})
					defer ctrl.Close()
					ref, err := ctrl.ContinueLegacySessionWithOptions(t.Context(), path, "", desktopLegacyImportOptions(root))
					if err != nil {
						t.Fatal(err)
					}
					workspaceID, err := app.attachDesktopSession(t.Context(), "project", root, ref)
					if err != nil {
						t.Fatal(err)
					}
					tab := &WorkspaceTab{ID: "migrated", Scope: "project", WorkspaceRoot: root,
						TopicID: topicID, SessionID: ref.SessionID, Ctrl: ctrl, Ready: true}
					tab.SessionWorkspace.ID = workspaceID
					if err := tab.ensureSessionLease(path); err != nil {
						t.Fatal(err)
					}
					defer tab.releaseSessionLease()
					keep := &WorkspaceTab{ID: "keep", Scope: "project", WorkspaceRoot: root, TopicID: "keep", Ready: true}
					app.tabs, app.tabOrder, app.activeTabID = map[string]*WorkspaceTab{keep.ID: keep}, []string{keep.ID}, keep.ID
					if detached {
						app.detachedSessions = map[string]*WorkspaceTab{tab.currentSessionIdentity(): tab}
					} else {
						app.tabs[tab.ID] = tab
						app.tabOrder = append(app.tabOrder, tab.ID)
						app.activeTabID = tab.ID
					}
					if !app.sessionOpen(dir, path) {
						t.Fatal("migrated compatibility lease must still mark its legacy file as open")
					}
					var archiveErr error
					if operation == "topic" {
						archiveErr = app.TrashTopic(topicID)
					} else {
						archiveErr = app.DeleteSession(path)
					}
					if err := archiveErr; err != nil {
						t.Fatalf("archive migrated owner: %v", err)
					}
					if !tab.removed || tab.sessionLeaseRuntimeKey() != "" {
						t.Fatal("archive retained the migrated runtime or its lease")
					}
					if _, err := os.Stat(path); err != nil {
						t.Fatalf("legacy source was not preserved: %v", err)
					}
					page, err := app.ListWorkspaceSessions(workspaceID, "", "", 10, true)
					if err != nil || len(page.Sessions) != 1 || !page.Sessions[0].Archived {
						t.Fatalf("archive state missing: %+v, %v", page, err)
					}
				})
			}
		})
	}
}
