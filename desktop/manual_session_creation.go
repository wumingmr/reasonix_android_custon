package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"reasonix/desktop/internal/sessionui"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/session"
)

type ManualSessionCreationRequest struct {
	OperationID   string `json:"operationId"`
	WorkspaceID   string `json:"workspaceId"`
	Scope         string `json:"scope,omitempty"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
}

type ManualSessionCreationView struct {
	OperationID   string                  `json:"operationId"`
	WorkspaceID   string                  `json:"workspaceId"`
	Scope         string                  `json:"scope"`
	WorkspaceRoot string                  `json:"workspaceRoot"`
	Ref           session.SessionRef      `json:"ref"`
	TopicID       string                  `json:"topicId"`
	Phase         string                  `json:"phase"`
	Error         string                  `json:"error,omitempty"`
	Settings      SessionDraftSettings    `json:"settings"`
	Progress      *ManualCreationProgress `json:"progress,omitempty"`
	SurfaceReady  bool                    `json:"surfaceReady,omitempty"`
}

func (a *App) sessionUIStore() *sessionui.Store {
	a.sessionServicesMu.Lock()
	defer a.sessionServicesMu.Unlock()
	if a.sessionUI == nil {
		a.sessionUI = sessionui.New(config.DesktopSessionUIStatePath())
	}
	return a.sessionUI
}

func (a *App) BeginManualSessionCreation(req ManualSessionCreationRequest) (result ManualSessionCreationView, err error) {
	defer func() { result = a.creationView(result) }()
	defer func() { err = sessionUIError(err, req.WorkspaceID, req.OperationID) }()
	if a.shuttingDown.Load() {
		return ManualSessionCreationView{}, errors.New("application is shutting down")
	}
	id := strings.TrimSpace(req.OperationID)
	if len(id) < 8 || len(id) > 128 {
		return ManualSessionCreationView{}, errors.New("invalid creation operation identity")
	}
	store := a.sessionUIStore()
	existing, err := store.Get(a.bootContext(), "creation", id)
	if err != nil {
		return ManualSessionCreationView{}, err
	}
	if existing.Revision != "0" {
		view, err := decodeManualCreation(existing)
		if err != nil {
			return view, err
		}
		return a.replayManualCreation(req, view)
	}
	workspaceID := req.WorkspaceID
	if workspaceID == "" {
		var err error
		workspaceID, err = a.ensureDesktopWorkspace(a.bootContext(), req.Scope, req.WorkspaceRoot)
		if err != nil {
			return ManualSessionCreationView{}, err
		}
	}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return ManualSessionCreationView{}, err
	}
	w, ok := state.Workspaces[workspaceID]
	if !ok && req.WorkspaceRoot != "" {
		w, err = workspacestate.ResolveCreationWorkspace(state, workspaceID, req.WorkspaceRoot)
		if err != nil {
			return ManualSessionCreationView{}, manualCreationTargetError(err)
		}
		workspaceID, ok = w.ID, true
	}
	if !ok {
		return ManualSessionCreationView{}, manualCreationTargetError(workspacestate.ErrWorkspaceNotFound)
	}
	scope, root := canonicalWorkspaceScope(w), w.Root
	if scope == "global" {
		root = ""
	}
	sum := sha256.Sum256([]byte(id))
	view := ManualSessionCreationView{OperationID: id, WorkspaceID: workspaceID, Scope: scope, WorkspaceRoot: root,
		Ref:     session.SessionRef{HostID: localDesktopHostID, SessionID: fmt.Sprintf("desktop-manual-%x", sum[:16])},
		TopicID: fmt.Sprintf("manual-%x", sum[:16]), Phase: "reserved", Settings: a.defaultDraftSettings(scope, root)}
	view.Settings.ModelSource = draftModelSourceExplicit
	payload, err := json.Marshal(view)
	if err != nil {
		return view, err
	}
	record, err := store.Save(a.bootContext(), "creation", id, "0", payload)
	if errors.Is(err, sessionui.ErrConflict) {
		view, err = decodeManualCreation(record)
		if err != nil {
			return view, err
		}
		return a.replayManualCreation(req, view)
	}
	if err != nil {
		return view, err
	}
	a.creationManager().Ensure(record.Key, "begin", "")
	return view, nil
}

func (a *App) GetManualSessionCreation(operationID string) (result ManualSessionCreationView, err error) {
	defer func() { result = a.creationView(result) }()
	defer func() { err = sessionUIError(err, "", operationID) }()
	r, err := a.sessionUIStore().Get(a.bootContext(), "creation", operationID)
	var view ManualSessionCreationView
	if err != nil {
		return view, err
	}
	if r.Revision == "0" {
		return view, errors.New("creation operation not found")
	}
	err = json.Unmarshal(r.Payload, &view)
	return view, err
}

func (a *App) RetryManualSessionCreation(operationID string) (result ManualSessionCreationView, err error) {
	defer func() { result = a.creationView(result) }()
	defer func() { err = sessionUIError(err, "", operationID) }()
	r, err := a.sessionUIStore().Get(a.bootContext(), "creation", operationID)
	var view ManualSessionCreationView
	if err != nil {
		return view, err
	}
	if r.Revision == "0" {
		return view, errors.New("creation operation not found")
	}
	if err = json.Unmarshal(r.Payload, &view); err != nil {
		return view, err
	}
	if view.Phase != "failed" && view.Phase != "reserved" && view.Phase != "starting" {
		return view, nil
	}
	a.creationManager().Ensure(r.Key, "retry", r.Revision)
	return view, nil
}

func (a *App) createManualSessionRuntime(ctx context.Context, view ManualSessionCreationView, report func(string)) error {
	tab, err := a.reserveManualSessionTab(ctx, view, report)
	if err != nil {
		return err
	}
	report("building_runtime")
	err = a.startManualSessionTab(ctx, tab, report)
	if err == nil {
		a.mu.Lock()
		tab.PendingCreateOperationID = ""
		a.saveTabsLocked()
		a.mu.Unlock()
	}
	return err
}

func (a *App) reserveManualSessionTab(ctx context.Context, view ManualSessionCreationView, report func(string)) (*WorkspaceTab, error) {
	report("waiting_runtime")
	defer a.lockRuntimeMutation("reserve manual session")()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.shuttingDown.Load() {
		return nil, errors.New("application is shutting down")
	}
	report("preparing_storage")
	root := desktopWorkspaceRoot(view.Scope, view.WorkspaceRoot)
	if view.Scope != "project" {
		if _, err := ensureGlobalWorkspaceRoot(); err != nil {
			return nil, err
		}
	}
	w, err := a.workspaceRegistry().BeginCreateAtRoot(ctx, workspacestate.PendingCreate{OperationID: view.OperationID, WorkspaceID: view.WorkspaceID, SessionID: view.Ref.SessionID}, root)
	if err != nil {
		return nil, manualCreationTargetError(err)
	}
	view.WorkspaceID, view.Scope, view.WorkspaceRoot = w.ID, canonicalWorkspaceScope(w), w.Root
	if view.Scope == "global" {
		view.WorkspaceRoot = ""
	}
	service := a.desktopSessionService("")
	if _, err := service.Query().Stat(ctx, view.Ref); errors.Is(err, session.ErrSessionNotFound) {
		runtime, createErr := service.Create(ctx, session.CreateOptions{SessionID: view.Ref.SessionID, CWD: desktopWorkspaceRoot(view.Scope, view.WorkspaceRoot), Origin: session.SessionOriginNew})
		if createErr != nil {
			return nil, createErr
		}
		if err := service.SetModel(ctx, view.Ref, view.Settings.Model, ""); err != nil {
			return nil, err
		}
		if _, err := runtime.Session().Flush(ctx); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	info, err := service.Query().Stat(ctx, view.Ref)
	if err != nil {
		return nil, err
	}
	same, err := sameDesktopPathStrict(info.CWD, root)
	if err != nil {
		return nil, manualCreationTargetError(workspacestate.ErrCreationWorkspaceUnavailable)
	}
	if !same || info.Origin == "" {
		return nil, manualCreationTargetError(workspacestate.ErrCreationWorkspaceChanged)
	}
	if err := a.workspaceRegistry().AttachCreatedSessionAtRoot(ctx, view.OperationID, view.WorkspaceID, view.Ref.SessionID, root); err != nil {
		return nil, manualCreationTargetError(err)
	}
	if err := a.workspaceRegistry().EnsureSessionTopic(ctx, view.Ref.SessionID, view.TopicID, ""); err != nil {
		return nil, err
	}
	a.emitProjectTreeChanged()
	if meta := a.metaForDraftSession(view.Ref.SessionID); meta != nil {
		tab, _ := a.tabAndCtrlByID(meta.ID)
		if tab != nil {
			return tab, nil
		}
	}
	settings := view.Settings
	tab := &WorkspaceTab{Scope: view.Scope, WorkspaceRoot: desktopWorkspaceRoot(view.Scope, view.WorkspaceRoot),
		SessionID: view.Ref.SessionID, TopicID: view.TopicID, TopicTitle: defaultTopicTitle, topicTitleSource: topicTitleSourceAuto,
		PendingCreateOperationID: view.OperationID, model: settings.Model, qualityFloor: settings.QualityFloor,
		mode:             tabModeFromAxes(tabModeHasPlan(settings.Mode), settings.ToolApprovalMode == control.ToolApprovalDangerFullAccess),
		toolApprovalMode: settings.ToolApprovalMode, disabledMCP: cloneServerViewMap(settings.DisabledMCP), mcpOrder: append([]string(nil), settings.MCPOrder...)}
	tab.SessionWorkspace.ID = view.WorkspaceID
	if settings.Effort != "" {
		effort := settings.Effort
		tab.effort = &effort
	}
	if err := createTopicState(view.WorkspaceRoot, view.TopicID, defaultTopicTitle, topicTitleSourceAuto, time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	a.mu.Lock()
	tab.ID = a.newUniqueTabIDLocked()
	tab.sink = &tabEventSink{tabID: tab.ID, app: a}
	a.tabs[tab.ID] = tab
	a.tabOrder = append(a.tabOrder, tab.ID)
	a.saveTabsLocked()
	a.mu.Unlock()
	return tab, nil
}
