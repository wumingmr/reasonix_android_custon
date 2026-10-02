package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/session"
	"reasonix/internal/taskmonitor"
)

func TestAuditCanonicalRestoreReturnsToSidebar(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = t.Context()
	pinDesktopSessionRoot(t, app)
	installNoopRuntimeEvents(app, nil)
	root := globalWorkspaceRoot()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	service := app.desktopSessionService("")
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "restore-audit", CWD: root, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	ref := runtime.Ref()
	payload, _ := json.Marshal(map[string]any{"message": map[string]any{"id": "user-1", "role": "user", "content": "preserved after restore"}})
	if _, err := runtime.Session().AppendBatch(t.Context(), "turn", []session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	workspaceID, err := app.attachDesktopSession(t.Context(), "global", "", ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	installSessionCatalogForTest(t, app, dir, "global", "")
	if err := app.ArchiveCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	archived, err := app.ListWorkspaceSessions(workspaceID, "", "", 10, true)
	if err != nil || len(archived.Sessions) != 1 || !archived.Sessions[0].Archived {
		t.Fatalf("archive fixture: %+v, %v", archived, err)
	}
	if err := app.RestoreCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	reconcileSessionCatalogForTest(t, app, dir, "global", "")
	page, err := app.ListWorkspaceSessions(workspaceID, "", "", 10, true)
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].Archived {
		t.Fatalf("restore registry: %+v, %v", page, err)
	}
	history, err := app.ReadSessionHistory(ref, "", 10)
	if err != nil || len(history.Messages) != 1 || history.Messages[0].Content != "preserved after restore" {
		t.Fatalf("restore history: %+v, %v", history, err)
	}
	t.Log("restore removed archive marker and preserved the full test history")
	if deleted := app.ListTrashedSessions(); len(deleted) != 0 {
		t.Fatalf("unexpected trash: %+v", deleted)
	}
	topics, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: "global", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(topics.Items) == 0 {
		t.Fatal("restore succeeded and history still exists, but sidebar has no restored session; archive and deleted lists are both empty")
	}
}

func auditMigratedTab(t *testing.T) (*App, *WorkspaceTab, *control.Controller, string) {
	t.Helper()
	isolateDesktopUserDirs(t)
	model, _ := configureSwitchableDefaultModels(t)
	app := NewApp()
	app.ctx = t.Context()
	app.readyHook = func() {}
	pinDesktopSessionRoot(t, app)
	root := canonicalRuntimeRoot(t.TempDir())
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := addProject(root, ""); err != nil {
		t.Fatal(err)
	}
	if err := setTopicTitle(root, "audit-topic", "Audit"); err != nil {
		t.Fatal(err)
	}
	path := writeTopicSessionWithPrompt(t, dir, "audit.jsonl", "audit-topic", "Audit", root, "preserved history", time.Now())
	ctrl := control.New(control.Options{
		WorkspaceRoot: root, SessionDir: dir,
		Executor:       agent.New(nil, nil, agent.NewSession("test"), agent.Options{}, event.Discard),
		SessionService: app.desktopSessionService(dir), ExclusiveSession: true,
		OnSessionRotation: app.prepareDesktopSessionRotation,
	})
	t.Cleanup(ctrl.Close)
	ref, err := ctrl.ContinueLegacySessionWithOptions(t.Context(), path, "", desktopLegacyImportOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	workspaceID, err := app.attachDesktopSession(t.Context(), "project", root, ref)
	if err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{ID: "audit", Scope: "project", WorkspaceRoot: root, TopicID: "audit-topic", SessionID: ref.SessionID, Ctrl: ctrl, Ready: true, model: model, disabledMCP: map[string]ServerView{}}
	tab.SessionWorkspace.ID = workspaceID
	tab.sink = &tabEventSink{tabID: tab.ID, app: app}
	installNoopRuntimeEvents(app, tab.sink)
	app.tabs[tab.ID], app.tabOrder, app.activeTabID = tab, []string{tab.ID}, tab.ID
	if err := tab.ensureSessionLease(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tab.releaseSessionLease)
	app.mu.Lock()
	app.newSessionRuntimeLocked(tab, sessionRuntimeKey(tab.currentSessionIdentity()))
	app.advanceSessionRuntimeEpochLocked(tab)
	app.mu.Unlock()
	return app, tab, ctrl, path
}

func TestAuditMigratedSessionReopen(t *testing.T) {
	app, tab, ctrl, path := auditMigratedTab(t)
	_, err := app.OpenTopicSession("project", tab.WorkspaceRoot, tab.TopicID, path)
	if err != nil {
		t.Fatalf("reopen current migrated session: %v", err)
	}
	if tab.Ctrl != ctrl {
		t.Fatal("reopening current migrated session replaced its controller")
	}
}

func TestAuditCanonicalIdleClearRegistry(t *testing.T) {
	app, tab, ctrl, _ := auditMigratedTab(t)
	oldRef, _ := ctrl.SessionRef()
	result, err := app.ClearSessionForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	newRef, _ := ctrl.SessionRef()
	if newRef == oldRef || result.SessionID != newRef.SessionID {
		t.Fatalf("rotation identities: old=%s new=%s result=%s", oldRef.SessionID, newRef.SessionID, result.SessionID)
	}
	app.mu.RLock()
	stale := app.liveRuntimeTabMatchingLocked(nil, sessionRoute(oldRef.SessionID))
	app.mu.RUnlock()
	if stale != nil {
		t.Errorf("old session lookup still returns controller now bound to %s", newRef.SessionID)
	}
	target := &WorkspaceTab{ID: "target", Scope: "project", WorkspaceRoot: tab.WorkspaceRoot, Ready: true}
	target.sink = &tabEventSink{tabID: target.ID, app: app}
	installNoopRuntimeEvents(app, target.sink)
	app.tabs[target.ID] = target
	app.tabOrder = append(app.tabOrder, target.ID)
	app.activeTabID = target.ID
	if _, err := app.OpenSession(oldRef); err != nil {
		t.Fatalf("reopen old canonical session: %v", err)
	}
	t.Cleanup(target.Ctrl.Close)
	actual, ok := target.Ctrl.(control.IdentityLifecycle).SessionRef()
	if !ok || actual != oldRef {
		t.Errorf("OpenSession returned success but controller=%s UI=%s requested=%s", actual.SessionID, target.SessionID, oldRef.SessionID)
	}
}

func TestAuditCanonicalTaskFilter(t *testing.T) {
	app, tab, _, _ := auditMigratedTab(t)
	for _, id := range []string{tab.SessionID, "other-session"} {
		if err := app.taskStore().SaveTask(t.Context(), tab.WorkspaceRoot, taskmonitor.TaskSnapshot{
			SchemaVersion: 1, TaskID: id + "--task-1", JobID: "task-1", SessionID: id,
			State: taskmonitor.TaskStateRunning, RuntimeState: taskmonitor.RuntimeStateAlive,
			Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := app.ListTasksForTab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Errorf("current-session task list includes %d sessions, want 1", len(tasks))
	}
	_, sessionID, err := app.taskProjectKeys(TaskPageRequest{Scope: "session", TabID: tab.ID})
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != tab.SessionID {
		t.Errorf("catalog session filter = %q, want %q", sessionID, tab.SessionID)
	}
}

type auditJobController struct {
	*control.Controller
	killed bool
}

func (c *auditJobController) CancelJob(string) bool      { c.killed = true; return true }
func (c *auditJobController) TaskRuntimeOwnerID() string { return "audit-runtime-owner" }

func TestAuditCanonicalTaskStop(t *testing.T) {
	app, tab, ctrl, _ := auditMigratedTab(t)
	wrapper := &auditJobController{Controller: ctrl}
	tab.Ctrl = wrapper
	id := tab.SessionID + "--task-1"
	if err := app.taskStore().SaveTask(t.Context(), tab.WorkspaceRoot, taskmonitor.TaskSnapshot{
		SchemaVersion: 1, TaskID: id, JobID: "task-1", SessionID: tab.SessionID,
		State: taskmonitor.TaskStateRunning, RuntimeState: taskmonitor.RuntimeStateAlive,
		RuntimeOwnerID: wrapper.TaskRuntimeOwnerID(),
		Version:        1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	result, err := app.StopTaskForTab(tab.ID, id, 1, "audit", "audit-stop")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted || !wrapper.killed {
		t.Fatalf("live canonical task was not stopped: accepted=%v error=%+v", result.Accepted, result.Error)
	}
}

func TestTaskControlRejectsOldRecorderGeneration(t *testing.T) {
	app, tab, ctrl, _ := auditMigratedTab(t)
	wrapper := &auditJobController{Controller: ctrl}
	tab.Ctrl = wrapper
	for _, owner := range []string{"", "previous-runtime-owner"} {
		id := tab.SessionID + "--task-1-" + owner
		if err := app.taskStore().SaveTask(t.Context(), tab.WorkspaceRoot, taskmonitor.TaskSnapshot{
			SchemaVersion: 1, TaskID: id, JobID: "task-1", SessionID: tab.SessionID,
			RuntimeOwnerID: owner, State: taskmonitor.TaskStateRunning, RuntimeState: taskmonitor.RuntimeStateAlive,
			Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		result, err := app.StopTaskForTab(tab.ID, id, 1, "test", "old-owner-"+owner)
		if err != nil || result.Accepted || wrapper.killed {
			t.Fatalf("old owner accepted: %+v %v", result, err)
		}
	}
}

func TestAuditMigratedCloseReleasesLease(t *testing.T) {
	app, tab, _, path := auditMigratedTab(t)
	keep := &WorkspaceTab{ID: "keep", Scope: "project", WorkspaceRoot: tab.WorkspaceRoot, Ready: true}
	app.tabs[keep.ID] = keep
	app.tabOrder = append(app.tabOrder, keep.ID)
	if err := app.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := agent.TryAcquireSessionLease(path)
	if err != nil {
		t.Fatalf("closed migrated tab retained legacy lease: %v", err)
	}
	lease.Release()
}

func TestAuditTopicArchiveCanonicalVisibility(t *testing.T) {
	app, tab, _, _ := auditMigratedTab(t)
	keep := &WorkspaceTab{ID: "keep", Scope: "project", WorkspaceRoot: tab.WorkspaceRoot, TopicID: "keep", Ready: true}
	app.tabs[keep.ID] = keep
	app.tabOrder = append(app.tabOrder, keep.ID)
	if err := app.TrashTopic(tab.TopicID); err != nil {
		t.Fatal(err)
	}
	page, err := app.ListWorkspaceSessions(tab.SessionWorkspace.ID, "", "", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range page.Sessions {
		if row.Ref.SessionID == tab.SessionID {
			t.Fatalf("archived topic remains visible as canonical session %s", row.Ref.SessionID)
		}
	}
}
