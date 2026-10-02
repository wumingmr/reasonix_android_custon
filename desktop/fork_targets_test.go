package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/session"
)

// forkTargetsStubController is a control.SessionAPI fake that also satisfies
// forkTargetsController and records the creation request it received.
type forkTargetsStubController struct {
	*tabScopedActionController
	running        bool
	set            session.ForkTargetSet
	setErr         error
	childID        string
	createErr      error
	createTurn     string
	createBoundary uint64
	createName     string
	createOp       string
	creates        int
	service        *session.Service
	cwd            string
}

// RuntimeStatus reports a turn in flight when running is set, so a scenario can
// assert the binding answers while the source is busy.
func (c *forkTargetsStubController) RuntimeStatus() control.RuntimeStatus {
	if !c.running {
		return c.tabScopedActionController.RuntimeStatus()
	}
	return control.RuntimeStatus{Running: true, Status: event.TurnInProgress}
}

func (c *forkTargetsStubController) ForkTargets() (session.ForkTargetSet, error) {
	return c.set, c.setErr
}

func (c *forkTargetsStubController) CreateForkSession(request session.ForkRequest, name string) (string, error) {
	c.creates++
	c.createTurn, c.createBoundary, c.createName, c.createOp = request.TurnID, request.BoundarySequence, name, request.OperationID
	if c.createErr == nil && c.service != nil {
		var runtime *session.Runtime
		runtime, c.createErr = c.service.Create(context.Background(), session.CreateOptions{
			SessionID: c.childID, CWD: c.cwd, ParentSessionID: request.Source.SessionID, Origin: session.SessionOriginFork,
		})
		if c.createErr == nil {
			c.createErr = c.service.Close(context.Background(), runtime.Ref())
		}
	}
	return c.childID, c.createErr
}

func enableForkTargetPersistence(t *testing.T, app *App, ctrl *forkTargetsStubController) {
	t.Helper()
	pinDesktopSessionRoot(t, app)
	ctrl.service = app.desktopSessionService("")
	ctrl.cwd = globalWorkspaceRoot()
}

func (c *forkTargetsStubController) UsesExclusiveSession() bool { return true }
func (c *forkTargetsStubController) SessionRef() (session.SessionRef, bool) {
	return session.SessionRef{HostID: "host-1", SessionID: "source-1"}, true
}
func (c *forkTargetsStubController) SessionService() *session.Service { return nil }
func (c *forkTargetsStubController) BindFreshSession(context.Context, string) (session.SessionRef, error) {
	return session.SessionRef{}, errors.New("not implemented")
}
func (c *forkTargetsStubController) OpenSession(context.Context, session.SessionRef) (session.SessionRef, error) {
	return session.SessionRef{}, errors.New("not implemented")
}
func (c *forkTargetsStubController) ContinueLegacySession(context.Context, string, string) (session.SessionRef, error) {
	return session.SessionRef{}, errors.New("not implemented")
}
func (c *forkTargetsStubController) ContinuePrototypeSession(context.Context, string) (session.SessionRef, error) {
	return session.SessionRef{}, errors.New("not implemented")
}

// assertEmptyForkTargets checks the shared empty result: no error, a non-nil
// slice, and a "targets" field that JSON-marshals to [] instead of null.
func assertEmptyForkTargets(t *testing.T, name string, view ForkTargetSetView, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: err = %v, want nil", name, err)
	}
	if view.Targets == nil {
		t.Fatalf("%s: Targets is nil; the renderer expects []", name)
	}
	if len(view.Targets) != 0 || view.Verifiable {
		t.Fatalf("%s: view = %+v, want an empty unverifiable set", name, view)
	}
	raw, marshalErr := json.Marshal(view)
	if marshalErr != nil {
		t.Fatalf("%s: marshal: %v", name, marshalErr)
	}
	var decoded struct {
		Targets *[]ForkTargetView `json:"targets"`
	}
	if unmarshalErr := json.Unmarshal(raw, &decoded); unmarshalErr != nil {
		t.Fatalf("%s: unmarshal %s: %v", name, raw, unmarshalErr)
	}
	if decoded.Targets == nil || len(*decoded.Targets) != 0 {
		t.Fatalf("%s: JSON = %s, want \"targets\":[]", name, raw)
	}
}

func TestForkTargetsForTabReturnsEmptyNonNilTargets(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	missing, missingErr := app.ForkTargetsForTab("missing")
	assertEmptyForkTargets(t, "missing tab", missing, missingErr)
	app.setTestCtrl(newTabScopedActionController(), "")
	stubbed, stubbedErr := app.ForkTargetsForTab("test")
	assertEmptyForkTargets(t, "controller without fork targets", stubbed, stubbedErr)
	active, activeErr := app.ForkTargetsForTab("")
	assertEmptyForkTargets(t, "active tab", active, activeErr)
}

func TestForkedSessionLocatorRejectsCatalogPseudoPaths(t *testing.T) {
	source := &WorkspaceTab{ID: "source"}
	for _, path := range []string{"", ".", "bare-session-id"} {
		if _, err := normalizeForkedSessionLocator(source, forkedSessionLocator{SessionPath: path}); err == nil {
			t.Fatalf("session path %q was accepted", path)
		}
	}
	if got, err := normalizeForkedSessionLocator(source, forkedSessionLocator{SessionID: "child-session"}); err != nil || got.SessionID != "child-session" {
		t.Fatalf("canonical session id = %+v, err=%v", got, err)
	}
	for _, path := range []string{"", ".", "child-session"} {
		if got := sessionDirectoryForPath(path); got != "" {
			t.Fatalf("sessionDirectoryForPath(%q) = %q, want no catalog target", path, got)
		}
	}
}

func TestForkTargetsForTabMapsTargetsForReadOnlyTab(t *testing.T) {
	isolateDesktopUserDirs(t)

	ctrl := &forkTargetsStubController{
		tabScopedActionController: newTabScopedActionController(),
		// A read-only channel tab whose turn is running still lists targets.
		running: true,
		set: session.ForkTargetSet{
			Source: session.SessionRef{HostID: "host-1", SessionID: "source-1"},
			Targets: []session.ForkTarget{
				{TurnID: "turn-1", BoundarySequence: 7, TurnNumber: 1, Status: event.TurnCompleted, MessageID: "msg-1", Available: true},
				{TurnID: "turn-2", TurnNumber: 2, Status: event.TurnInProgress, Reason: session.ForkTurnOpen},
			},
			Verifiable: true,
		},
	}
	app := NewApp()
	app.setTestCtrl(ctrl, "")
	app.tabs["test"].SessionID = "source-1"
	app.tabs["test"].Scope = "global"
	app.tabs["test"].ReadOnly = true

	view, err := app.ForkTargetsForTab("test")
	if err != nil {
		t.Fatalf("ForkTargetsForTab: %v", err)
	}
	if !view.Verifiable {
		t.Fatal("Verifiable = false, want the controller's value")
	}
	want := []ForkTargetView{
		{SourceHostID: "host-1", SourceSessionID: "source-1", TurnID: "turn-1", BoundarySequence: 7, TurnNumber: 1, Status: string(event.TurnCompleted), MessageID: "msg-1", Available: true},
		{SourceHostID: "host-1", SourceSessionID: "source-1", TurnID: "turn-2", TurnNumber: 2, Status: string(event.TurnInProgress), Reason: string(session.ForkTurnOpen)},
	}
	if len(view.Targets) != len(want) {
		t.Fatalf("targets = %+v, want %+v", view.Targets, want)
	}
	for i, target := range view.Targets {
		if target != want[i] {
			t.Fatalf("target[%d] = %+v, want %+v", i, target, want[i])
		}
	}
	raw, marshalErr := json.Marshal(view.Targets[1])
	if marshalErr != nil {
		t.Fatalf("marshal target: %v", marshalErr)
	}
	var fields map[string]any
	if unmarshalErr := json.Unmarshal(raw, &fields); unmarshalErr != nil {
		t.Fatalf("unmarshal target: %v", unmarshalErr)
	}
	if _, present := fields["messageId"]; present {
		t.Fatalf("target without a message id = %s, want messageId omitted", raw)
	}
}

func TestForkTargetsForTabReturnsControllerError(t *testing.T) {
	isolateDesktopUserDirs(t)

	ctrl := &forkTargetsStubController{
		tabScopedActionController: newTabScopedActionController(),
		setErr:                    errors.New("fork targets unavailable"),
	}
	app := NewApp()
	app.setTestCtrl(ctrl, "")

	view, err := app.ForkTargetsForTab("test")
	if err == nil {
		t.Fatal("ForkTargetsForTab: err = nil, want the controller's failure")
	}
	// The error is asserted above; the view still satisfies the empty-set contract.
	assertEmptyForkTargets(t, "failed targets", view, nil)
}

func TestCreateForkForTabOpensChildInNewTab(t *testing.T) {
	isolateDesktopUserDirs(t)

	childID := "created-fork"
	ctrl := &forkTargetsStubController{
		tabScopedActionController: newTabScopedActionController(),
		// Creating a child neither stops the running turn nor takes a rotation gate.
		running: true,
		childID: childID,
	}
	app := NewApp()
	app.setTestCtrl(ctrl, "")
	enableForkTargetPersistence(t, app, ctrl)
	app.tabs["test"].SessionID = "source-1"
	app.tabs["test"].Scope = "global"
	app.tabs["test"].TopicTitle = "Source topic"
	// A read-only channel tab is a legitimate fork source: the child is written
	// from the source, never into it.
	app.tabs["test"].ReadOnly = true

	view, err := app.CreateForkForTab("test", ForkAnchorView{SourceHostID: "host-1", SourceSessionID: "source-1", TurnID: "turn-7", BoundarySequence: 9})
	if err != nil {
		t.Fatalf("CreateForkForTab: %v", err)
	}
	if !view.Opened || view.Error != "" {
		t.Fatalf("view = %+v, want an opened tab without an error", view)
	}
	if view.SessionID != childID {
		t.Fatalf("sessionId = %q, want %q", view.SessionID, childID)
	}
	if view.TabID == "" || view.TabID == "test" {
		t.Fatalf("tabId = %q, want a fresh tab", view.TabID)
	}
	if ctrl.createTurn != "turn-7" || ctrl.createBoundary != 9 || ctrl.createName != "" || ctrl.createOp == "" || view.OperationID != ctrl.createOp {
		t.Fatalf("create request = (%q, %d, %q, %q), want the anchored turn and host operation",
			ctrl.createTurn, ctrl.createBoundary, ctrl.createName, ctrl.createOp)
	}
	if app.tabs["test"] == nil || app.tabs["test"].Ctrl != ctrl {
		t.Fatal("source tab lost its controller")
	}
	if app.activeTabID != view.TabID {
		t.Fatalf("active tab = %q, want the focused source's child %q", app.activeTabID, view.TabID)
	}
	child := app.tabs[view.TabID]
	if child == nil {
		t.Fatalf("child tab %q is missing", view.TabID)
	}
	if child.TopicID == "" || child.SessionID != childID || child.SessionPath != "" {
		t.Fatalf("child tab = %+v, want canonical session id %q", child, childID)
	}
}

func TestRepeatedForkPublishesSidebarMembershipWithoutRestart(t *testing.T) {
	for _, scope := range []string{"global", "project"} {
		t.Run(scope, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			ctrl := &forkTargetsStubController{tabScopedActionController: newTabScopedActionController(), childID: "fork-one"}
			app := NewApp()
			app.ctx = context.Background()
			app.setTestCtrl(ctrl, "")
			enableForkTargetPersistence(t, app, ctrl)
			root := ""
			if scope == "project" {
				root = t.TempDir()
				ctrl.cwd = root
			}
			app.tabs["test"].SessionID = "source-1"
			app.tabs["test"].Scope = scope
			app.tabs["test"].WorkspaceRoot = root
			app.tabs["test"].TopicTitle = "Source topic"
			events := make(chan ProjectTreeChangedV2, 4)
			app.runtimeEvents.emit = func(_ context.Context, name string, payload ...any) {
				if name == "project-tree:changed-v2" && len(payload) == 1 {
					events <- payload[0].(ProjectTreeChangedV2)
				}
			}
			lastRevision := uint64(0)
			checkEvent := func(child string) {
				t.Helper()
				select {
				case event := <-events:
					if event.Revision <= lastRevision || len(event.Roots) != 1 || event.Roots[0] != root {
						t.Fatalf("fork %q sidebar event = %+v, want root %q", child, event, root)
					}
					lastRevision = event.Revision
				case <-time.After(5 * time.Second):
					t.Fatalf("fork %q did not invalidate the sidebar topic page", child)
				}
			}

			anchor := ForkAnchorView{SourceHostID: "host-1", SourceSessionID: "source-1", TurnID: "turn-7", BoundarySequence: 9}
			first, err := app.CreateForkForTab("test", anchor)
			if err != nil || !first.Opened {
				t.Fatalf("first fork = %+v, err = %v", first, err)
			}
			checkEvent(first.SessionID)
			checkEvent(first.SessionID)
			if err := app.AcknowledgeForkOperation("test", first.OperationID); err != nil {
				t.Fatal(err)
			}
			// The user returns to the original conversation before forking again.
			app.mu.Lock()
			app.activeTabID = "test"
			app.mu.Unlock()
			ctrl.childID = "fork-two"
			second, err := app.CreateForkForTab("test", anchor)
			if err != nil || !second.Opened || second.SessionID == first.SessionID {
				t.Fatalf("second fork = %+v, err = %v", second, err)
			}
			checkEvent(second.SessionID)
			checkEvent(second.SessionID)
			page, err := app.ListProjectTopics(ProjectTopicPageRequest{Scope: scope, WorkspaceRoot: root, Limit: 20})
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			wantTitle := app.forkTopicTitle("Source topic")
			for _, item := range page.Items {
				if item.Session != nil {
					seen[item.Session.SessionID] = true
					if (item.Session.SessionID == first.SessionID || item.Session.SessionID == second.SessionID) && item.Label != wantTitle {
						t.Fatalf("fork %q label = %q, want %q", item.Session.SessionID, item.Label, wantTitle)
					}
				}
			}
			if !seen[first.SessionID] || !seen[second.SessionID] {
				t.Fatalf("sidebar page lacks repeated forks: %+v", page.Items)
			}
		})
	}
}

func TestCreateForkForTabKeepsChildWhenTabAttachFails(t *testing.T) {
	isolateDesktopUserDirs(t)

	childID := "orphan-fork"
	ctrl := &forkTargetsStubController{
		tabScopedActionController: newTabScopedActionController(),
		childID:                   childID,
	}
	app := NewApp()
	app.ctx = context.Background()
	app.setTestCtrl(ctrl, "")
	enableForkTargetPersistence(t, app, ctrl)
	app.tabs["test"].SessionID = "source-1"
	app.tabs["test"].Scope = "global"
	app.tabs["test"].TopicTitle = "Source topic"
	changed := make(chan ProjectTreeChangedV2, 4)
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...any) {
		if name == "project-tree:changed-v2" && len(payload) == 1 {
			changed <- payload[0].(ProjectTreeChangedV2)
		}
	}
	t.Cleanup(func() { forkTabBeforePublishHookForTest.Store(nil) })
	// Closing the source tab mid-flight makes the attach a no-op, which is the
	// same outcome as an attach that fails outright.
	hook := func() {
		app.mu.Lock()
		delete(app.tabs, "test")
		app.removeTabOrderLocked("test")
		if app.activeTabID == "test" {
			app.activeTabID = ""
		}
		app.mu.Unlock()
	}
	forkTabBeforePublishHookForTest.Store(&hook)

	view, err := app.CreateForkForTab("test", ForkAnchorView{SourceHostID: "host-1", SourceSessionID: "source-1", TurnID: "turn-7", BoundarySequence: 9})
	if err != nil {
		t.Fatalf("CreateForkForTab: %v", err)
	}
	if view.Opened {
		t.Fatal("opened = true, want false when the new tab was not created")
	}
	// The invalidation must include the persisted title, even if an earlier
	// membership event already caused the frontend to cache a placeholder row.
	wantRevision := app.unifiedProjectRevision(app.currentSessionCatalogStatus().Revision)
	deadline := time.After(5 * time.Second)
	for refreshed := false; !refreshed; {
		select {
		case event := <-changed:
			refreshed = event.Revision >= wantRevision
		case <-deadline:
			t.Fatal("durable fork presentation did not invalidate the sidebar after tab attach failed")
		}
	}
	if view.TabID != "" {
		t.Fatalf("tabId = %q, want empty", view.TabID)
	}
	if view.SessionID != childID {
		t.Fatalf("sessionId = %q, want the created child %q", view.SessionID, childID)
	}
	if view.Error == "" {
		t.Fatal("error is empty; the caller cannot offer a recovery entry")
	}
	if ctrl.creates != 1 {
		t.Fatalf("creates = %d, want exactly one child", ctrl.creates)
	}
	if ctrl.createOp == "" || view.OperationID != ctrl.createOp {
		t.Fatalf("operationId = %q view=%q, want one host-owned id", ctrl.createOp, view.OperationID)
	}
}

func TestCreateForkForTabReturnsCreateFailure(t *testing.T) {
	isolateDesktopUserDirs(t)

	ctrl := &forkTargetsStubController{
		tabScopedActionController: newTabScopedActionController(),
		createErr:                 errors.New("turn is not forkable"),
	}
	app := NewApp()
	app.setTestCtrl(ctrl, "")
	app.tabs["test"].SessionID = "source-1"
	app.tabs["test"].Scope = "global"

	view, err := app.CreateForkForTab("test", ForkAnchorView{SourceHostID: "host-1", SourceSessionID: "source-1", TurnID: "turn-7", BoundarySequence: 9})
	if err == nil {
		t.Fatal("CreateForkForTab: err = nil, want the controller's failure")
	}
	if view.SessionID != "" || view.TabID != "" || view.Opened || view.Error != "" {
		t.Fatalf("view = %+v, want the zero view alongside the error", view)
	}
	if len(app.tabs) != 1 || app.tabs["test"] == nil || app.activeTabID != "test" {
		t.Fatalf("tabs = %d, active = %q, want only the unchanged source tab", len(app.tabs), app.activeTabID)
	}
	journal, loadErr := loadForkOperations(forkOperationsPath())
	if loadErr != nil || len(journal.Operations) != 1 || journal.Operations[0].State != "pending" {
		t.Fatalf("uncertain failure journal = %+v, err=%v", journal, loadErr)
	}
}

func TestCreateForkForTabDiscardsExplicitRefusal(t *testing.T) {
	isolateDesktopUserDirs(t)
	ctrl := &forkTargetsStubController{tabScopedActionController: newTabScopedActionController(),
		createErr: &session.ForkUnavailableError{TurnID: "turn-7", Reason: session.ForkActiveAuthority}}
	app := NewApp()
	app.setTestCtrl(ctrl, "")
	app.tabs["test"].SessionID = "source-1"
	view, err := app.CreateForkForTab("test", ForkAnchorView{SourceHostID: "host-1", SourceSessionID: "source-1",
		TurnID: "turn-7", BoundarySequence: 9})
	if err != nil || view.Reason != string(session.ForkActiveAuthority) {
		t.Fatalf("explicit refusal = %+v, err=%v", view, err)
	}
	journal, loadErr := loadForkOperations(forkOperationsPath())
	if loadErr != nil || len(journal.Operations) != 0 {
		t.Fatalf("explicit refusal journal = %+v, err=%v", journal, loadErr)
	}
}

func TestCreateForkForTabRejectsStaleSourceIdentity(t *testing.T) {
	isolateDesktopUserDirs(t)
	ctrl := &forkTargetsStubController{tabScopedActionController: newTabScopedActionController(), childID: "must-not-exist"}
	app := NewApp()
	app.setTestCtrl(ctrl, "")
	app.tabs["test"].Scope = "global"
	app.tabs["test"].SessionID = "source-b"

	view, err := app.CreateForkForTab("test", ForkAnchorView{SourceHostID: "host-1", SourceSessionID: "source-a",
		TurnID: "shared-turn", BoundarySequence: 9})
	if err != nil || view.Reason != string(session.ForkStaleSource) || ctrl.creates != 0 {
		t.Fatalf("stale create = %+v, err=%v creates=%d", view, err, ctrl.creates)
	}
	journal, loadErr := loadForkOperations(forkOperationsPath())
	if loadErr != nil || len(journal.Operations) != 0 {
		t.Fatalf("stale create journal = %+v, err=%v", journal, loadErr)
	}
}

func TestForkOperationJournalSurvivesRestartAndAcknowledgement(t *testing.T) {
	isolateDesktopUserDirs(t)
	template := forkOperation{Surface: "remote", TabID: "tab", SourceHostID: "host", SourceSessionID: "source",
		TurnID: "turn", BoundarySequence: 12}
	first, err := (&App{}).beginForkOperation(template)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).AcknowledgeForkOperation("tab", first.OperationID); err != nil {
		t.Fatal(err)
	}
	second, err := (&App{}).beginForkOperation(template)
	if err != nil || second.OperationID != first.OperationID || second.State != "pending" {
		t.Fatalf("reloaded pending = %+v, err=%v; want %+v", second, err, first)
	}
	if err := (&App{}).completeForkOperation(first.OperationID, "child"); err != nil {
		t.Fatal(err)
	}
	restartedTemplate := template
	restartedTemplate.TabID = "tab-after-restart"
	completed, err := (&App{}).beginForkOperation(restartedTemplate)
	if err != nil || completed.OperationID != first.OperationID || completed.State != "completed" || completed.ChildSessionID != "child" {
		t.Fatalf("reloaded completion = %+v, err=%v", completed, err)
	}
	if err := (&App{}).AcknowledgeForkOperation("tab-after-restart", first.OperationID); err != nil {
		t.Fatal(err)
	}
	fresh, err := (&App{}).beginForkOperation(template)
	if err != nil || fresh.OperationID == first.OperationID {
		t.Fatalf("fresh operation after acknowledgement = %+v, err=%v", fresh, err)
	}
}

func TestCreateForkForTabReopensCompletedOperationAfterAttachFailure(t *testing.T) {
	isolateDesktopUserDirs(t)
	ctrl := &forkTargetsStubController{tabScopedActionController: newTabScopedActionController(), childID: "recovered-child"}
	app := NewApp()
	app.setTestCtrl(ctrl, "")
	enableForkTargetPersistence(t, app, ctrl)
	app.tabs["test"].Scope = "global"
	app.tabs["test"].SessionID = "source-1"
	anchor := ForkAnchorView{SourceHostID: "host-1", SourceSessionID: "source-1", TurnID: "turn", BoundarySequence: 9}
	t.Cleanup(func() { forkTabBeforePublishHookForTest.Store(nil) })
	hook := func() {
		app.mu.Lock()
		app.tabs["test"] = &WorkspaceTab{ID: "test", Scope: "global", TopicTitle: "Source",
			SessionID: "source-1", Ctrl: ctrl}
		app.mu.Unlock()
		forkTabBeforePublishHookForTest.Store(nil)
	}
	forkTabBeforePublishHookForTest.Store(&hook)
	first, err := app.CreateForkForTab("test", anchor)
	if err != nil || first.Opened || first.SessionID != "recovered-child" || ctrl.creates != 1 {
		t.Fatalf("first attach = %+v err=%v creates=%d", first, err, ctrl.creates)
	}
	// The durable row is usable even when opening its tab failed. A retry
	// must preserve a title chosen through that row in the meantime.
	chosenTitle := "Chosen fork title"
	if err := app.workspaceRegistry().UpdatePresentation(context.Background(), []string{first.SessionID}, &chosenTitle, nil); err != nil {
		t.Fatal(err)
	}
	second, err := app.CreateForkForTab("test", anchor)
	if err != nil || !second.Opened || second.SessionID != first.SessionID || second.OperationID != first.OperationID || ctrl.creates != 1 {
		t.Fatalf("recovered attach = %+v err=%v creates=%d; first=%+v", second, err, ctrl.creates, first)
	}
	state, err := app.workspaceRegistry().Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	presentation := state.Presentation[second.SessionID]
	app.mu.RLock()
	tab := app.tabs[second.TabID]
	topicID, title := tab.TopicID, tab.TopicTitle
	app.mu.RUnlock()
	if topicID != presentation.TopicID || title != presentation.Title {
		t.Fatalf("retried tab presentation = %q / %q, durable sidebar = %q / %q", topicID, title, presentation.TopicID, presentation.Title)
	}
}
