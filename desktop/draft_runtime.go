package main

import (
	"errors"
	"reflect"
	"strings"

	"reasonix/desktop/internal/draftstate"
	"reasonix/internal/config"
	"reasonix/internal/control"
)

type draftAdmissionProfile struct {
	submissionID string
	settings     SessionDraftSettings
	controller   control.SessionAPI
}

func (a *App) validateDraftAdmission(tab *WorkspaceTab, submissionID string) error {
	a.mu.RLock()
	operationID, profile := tab.PendingCreateOperationID, tab.draftAdmission
	a.mu.RUnlock()
	if operationID == "" || !strings.HasPrefix(operationID, "draft-op-") && profile == nil {
		return nil
	}
	if profile == nil || profile.submissionID != submissionID {
		return errors.New("draft creation owns the first submission")
	}
	if a.controllerForTab(tab) != profile.controller {
		return errors.New("draft execution owner changed before admission")
	}
	return a.verifyDraftRuntime(tab.ID, profile.settings)
}

// prepareDraftRuntime shares the rebuild/turn barriers with formal sessions.
// No metadata promises a profile until its actual execution owner has it.
func (a *App) prepareDraftRuntime(op draftstate.Operation, settings SessionDraftSettings) error {
	a.runtimeRebuildMu.Lock()
	defer a.runtimeRebuildMu.Unlock()
	a.mu.RLock()
	var tab *WorkspaceTab
	for _, candidate := range a.runtimeTabsLocked() {
		if candidate.SessionID == op.SessionID {
			tab = candidate
			break
		}
	}
	a.mu.RUnlock()
	if tab == nil {
		return nil
	}
	tab.turnStartMu.Lock()
	defer tab.turnStartMu.Unlock()
	snap := a.tabRuntimeSnapshot(tab)
	if snap.ctrl == nil {
		a.mu.Lock()
		a.publishDraftSettingsLocked(op, settings)
		a.mu.Unlock()
		return nil
	}
	if err := rebuildControllerActiveWorkErrorFor(snap.ctrl, "draft configuration"); err != nil {
		return err
	}
	a.mu.RLock()
	sameTools := reflect.DeepEqual(tab.disabledMCP, settings.DisabledMCP) || len(tab.disabledMCP) == 0 && len(settings.DisabledMCP) == 0
	sameOrder := reflect.DeepEqual(tab.mcpOrder, settings.MCPOrder) || len(tab.mcpOrder) == 0 && len(settings.MCPOrder) == 0
	a.mu.RUnlock()
	effort := ""
	if snap.effort != nil {
		effort = *snap.effort
	}
	runtime := normalizedTabRuntime{collaborationMode: settings.CollaborationMode, toolApprovalMode: normalizeToolApprovalMode(settings.ToolApprovalMode), qualityFloor: settings.QualityFloor, tokenMode: snap.tokenMode}
	if runtime.collaborationMode == "" && tabModeHasPlan(settings.Mode) {
		runtime.collaborationMode = "plan"
	}
	// These setters cannot fail and run under the same turn admission barrier.
	// Construction-dependent settings use a candidate rather than piecemeal RPCs.
	if snap.model == settings.Model && effort == settings.Effort && sameTools && sameOrder {
		release, err := a.lockDraftRuntimePublication(op.ID)
		if err != nil {
			return err
		}
		defer release()
		configureControllerRuntime(snap.ctrl, nil, runtime)
		a.mu.Lock()
		defer a.mu.Unlock()
		if tab.Ctrl != snap.ctrl || !a.ownsRuntimeTabLocked(tab) {
			return errors.New("draft runtime changed")
		}
		a.publishDraftSettingsLocked(op, settings)
		return nil
	}
	cfg, err := config.LoadForRootReadOnly(snap.workspaceRoot)
	if err != nil {
		return err
	}
	model, err := resolveDraftCreateModelStrict(cfg, settings.Model)
	if err != nil {
		return err
	}
	options := a.sessionOpenBootOptions(tab, snap, cfg, a.desktopSessionService(sessionDirForSnapshot(snap)), a.lookupSharedHost(snap.sharedHostKey), snap.workspaceRoot, model)
	options.ConfigSnapshot = cfg
	options.EffortOverride = nil
	if settings.Effort != "" {
		value := settings.Effort
		options.EffortOverride = &value
	}
	candidate, bound, err := buildDesktopControllerReplacement(a.bootContext(), snap.ctrl, options)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			discardReplacementController(candidate, snap.ctrl)
		}
	}()
	if !bound {
		return errors.New("draft retry requires the original canonical session runtime")
	}
	configureControllerRuntime(candidate, snap.ctrl, runtime)
	for name := range settings.DisabledMCP {
		candidate.UnregisterMCPServerTools(name)
	}
	if _, err := normalizeRestoredControllerRuntime(candidate, runtime); err != nil {
		return err
	}
	// Cancellation is durable and checked off App.mu, before compare-and-publish.
	release, err := a.lockDraftRuntimePublication(op.ID)
	if err != nil {
		return err
	}
	defer release()
	a.mu.Lock()
	if tab.Ctrl != snap.ctrl || !a.ownsRuntimeTabLocked(tab) || tab.SessionID != op.SessionID {
		a.mu.Unlock()
		return errors.New("draft runtime changed while preparing configuration")
	}
	a.mu.Unlock()
	if err := activateReplacementController(snap.ctrl, candidate); err != nil {
		return err
	}
	a.mu.Lock()
	tab.Ctrl = candidate
	a.supersedeTabBuildLocked(tab)
	a.publishDraftSettingsLocked(op, settings)
	a.mu.Unlock()
	committed = true
	retireReplacedController(snap.ctrl, candidate)
	a.notifyTabRuntimeRebuilt(tab)
	return nil
}

func (a *App) lockDraftRuntimePublication(operationID string) (func(), error) {
	if !strings.HasPrefix(operationID, "draft-op-") {
		return func() {}, nil
	}
	release, err := a.draftStore().PublicationLease(a.bootContext(), operationID)
	if err != nil {
		return nil, err
	}
	op, err := a.draftStore().Operation(a.bootContext(), operationID)
	if err != nil || op.Phase != "starting" {
		release()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("draft operation no longer owns runtime preparation")
	}
	return release, nil
}

func (a *App) verifyDraftRuntime(tabID string, settings SessionDraftSettings) error {
	tab, ctrl := a.tabAndCtrlByID(tabID)
	if tab == nil || ctrl == nil {
		return errors.New("draft runtime is unavailable")
	}
	snap := a.tabRuntimeSnapshot(tab)
	effort := ""
	if snap.effort != nil {
		effort = *snap.effort
	}
	a.mu.RLock()
	toolsMatch := (reflect.DeepEqual(tab.disabledMCP, settings.DisabledMCP) || len(tab.disabledMCP) == 0 && len(settings.DisabledMCP) == 0) && (reflect.DeepEqual(tab.mcpOrder, settings.MCPOrder) || len(tab.mcpOrder) == 0 && len(settings.MCPOrder) == 0)
	a.mu.RUnlock()
	if effort != settings.Effort || !toolsMatch || snap.qualityFloor != settings.QualityFloor {
		return errors.New("draft execution profile changed before admission")
	}
	model := strings.TrimSpace(settings.Model)
	if strings.TrimSpace(snap.model) != model {
		cfg, err := config.LoadForRootReadOnly(snap.workspaceRoot)
		if err != nil {
			return err
		}
		model, err = resolveDraftCreateModelStrict(cfg, model)
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(snap.model) != model || normalizeToolApprovalMode(ctrl.ToolApprovalMode()) != normalizeToolApprovalMode(settings.ToolApprovalMode) {
		return errors.New("draft runtime configuration changed before admission")
	}
	if actual, ok := ctrl.(interface{ ModelRef() string }); ok && actual.ModelRef() != model {
		cfg, err := config.LoadForRootReadOnly(snap.workspaceRoot)
		if err != nil {
			return err
		}
		canonical, err := resolveDraftCreateModelStrict(cfg, settings.Model)
		if err != nil || actual.ModelRef() != canonical {
			return errors.New("draft Controller model differs from its frozen configuration")
		}
	}
	if ctrl.PlanMode() != (settings.CollaborationMode == "plan" || settings.CollaborationMode == "" && tabModeHasPlan(settings.Mode)) {
		return errors.New("draft collaboration mode changed before admission")
	}
	return nil
}
