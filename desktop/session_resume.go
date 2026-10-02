package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/control"
	"reasonix/internal/session"
)

var errSessionNavigationSuperseded = errors.New("session navigation was superseded")

func (a *App) continueLegacySessionForTranscript(tab *WorkspaceTab, ctrl control.SessionAPI, sourcePath string, limit int, includeHistory, readOnly bool) (HistoryPage, error) {
	identity, ok := ctrl.(control.IdentityLifecycle)
	if !ok || !identity.UsesExclusiveSession() {
		return HistoryPage{}, fmt.Errorf("session identity protocol is unavailable")
	}
	navigationCtx, finishNavigation := a.beginSessionNavigationContext()
	defer finishNavigation()
	if err := context.Cause(navigationCtx); err != nil {
		return HistoryPage{}, err
	}
	if _, adopted, err := a.legacyCanonicalRef(navigationCtx, sourcePath); err != nil {
		return HistoryPage{}, err
	} else if !adopted {
		// Explicit navigation imports before taking any controller swap gate.
		if _, err := a.ImportHistoricalSession(desktopSourceKey(sourcePath, "")); err != nil {
			return HistoryPage{}, err
		}
	}

	a.runtimeRebuildMu.Lock()
	defer a.runtimeRebuildMu.Unlock()
	tab.turnStartMu.Lock()
	defer tab.turnStartMu.Unlock()
	if err := context.Cause(navigationCtx); err != nil {
		return HistoryPage{}, err
	}

	current := a.controllerForTab(tab)
	if current != ctrl || current == nil {
		return HistoryPage{}, fmt.Errorf("tab runtime changed while continuing legacy session")
	}
	if current.RuntimeStatus().Running || current.RuntimeStatus().PendingPrompt {
		return HistoryPage{}, control.ErrTurnRunning
	}
	if !historicalPreview(current) {
		if err := current.Snapshot(); err != nil {
			return HistoryPage{}, err
		}
	}
	a.mu.RLock()
	createOptions := desktopLegacyImportOptions(snapshotTabRuntimeLocked(tab).workspaceRoot)
	a.mu.RUnlock()
	_, err := a.openOrImportDesktopLegacySession(navigationCtx, identity, sourcePath, createOptions)
	if err != nil {
		return HistoryPage{}, err
	}
	a.syncTabSessionIdentity(tab, current)
	a.setTabReadOnly(tab.ID, readOnly)
	a.invalidatePromptHistoryCache()
	a.notifyTabRuntimeRebuilt(tab)
	if !includeHistory {
		return HistoryPage{Messages: []HistoryMessage{}}, nil
	}
	return historyPageFromMessagesForTab(tab, current, current.History(), 0, limit), nil
}

// canonicalOpenIdentity validates the identity protocol of a resolved runtime.
// A nil runtime is a dormant tab, not a protocol failure.
func canonicalOpenIdentity(ctrl control.SessionAPI) (control.IdentityLifecycle, error) {
	if ctrl == nil {
		return nil, nil
	}
	// A native historical JSONL runtime is a supported source, but has no
	// canonical binding to reuse. Let the existing prepare-and-swap path build
	// the destination runtime without closing or converting the source first.
	if native, ok := ctrl.(*control.Controller); ok && native.NativeLegacySession() {
		return nil, nil
	}
	identity, ok := ctrl.(control.IdentityLifecycle)
	if !ok || !identity.UsesExclusiveSession() {
		return nil, fmt.Errorf("session identity protocol is unavailable")
	}
	return identity, nil
}

func (a *App) resumeCanonicalSessionForTranscript(tab *WorkspaceTab, ctrl control.SessionAPI, route string, limit int, includeHistory bool, navigationSequence ...uint64) (HistoryPage, error) {
	identity, err := canonicalOpenIdentity(ctrl)
	if err != nil {
		return HistoryPage{}, err
	}
	service := a.desktopSessionService("")
	ref, ok := sessionRefForRoute(service, route)
	if !ok {
		return HistoryPage{}, fmt.Errorf("invalid session identity")
	}
	navigationCtx, finishNavigation := a.beginSessionNavigationContext(navigationSequence...)
	defer finishNavigation()
	if err := context.Cause(navigationCtx); err != nil {
		return HistoryPage{}, err
	}
	workspace, err := a.canonicalSessionWorkspace(navigationCtx, ref)
	if err != nil {
		return HistoryPage{}, err
	}

	a.runtimeRebuildMu.Lock()
	defer a.runtimeRebuildMu.Unlock()
	tab.turnStartMu.Lock()
	defer tab.turnStartMu.Unlock()
	wantedNavigation := uint64(0)
	if len(navigationSequence) > 0 {
		wantedNavigation = navigationSequence[0]
		if a.desktopSessions.navigationSeq.Load() != wantedNavigation {
			return HistoryPage{}, errSessionNavigationSuperseded
		}
	}
	if err := context.Cause(navigationCtx); err != nil {
		return HistoryPage{}, err
	}

	current := a.controllerForTab(tab)
	if ctrl == nil {
		// The caller resolved a dormant tab before it had a runtime. Adopting a
		// concurrently built one here, under the rebuild lock, is the
		// authoritative read; the fences below still reject a later build.
		ctrl = current
		if identity, err = canonicalOpenIdentity(ctrl); err != nil {
			return HistoryPage{}, err
		}
	}
	if current != ctrl {
		return HistoryPage{}, fmt.Errorf("tab runtime changed while opening session")
	}
	var currentRef session.SessionRef
	if identity != nil {
		currentRef, _ = identity.SessionRef()
	}
	workspaceChanged := canonicalWorkspaceChanged(a.tabRuntimeSnapshot(tab), workspace)
	if current == nil || currentRef != ref || workspaceChanged {
		if current != nil && !controllerHasActiveRuntimeWork(current) && !historicalPreview(current) {
			if err := current.Snapshot(); err != nil {
				return HistoryPage{}, err
			}
		}
		adopted, err := a.reattachCanonicalSessionRuntime(tab, current, ref, workspace, wantedNavigation)
		if err != nil {
			return HistoryPage{}, err
		}
		if adopted != nil {
			current = adopted
		} else {
			binding, err := service.EnsureExecution(navigationCtx, ref)
			if err != nil {
				return HistoryPage{}, err
			}
			defer func() { _ = binding.Release(a.bootContext()) }()
			targetModel := strings.TrimSpace(binding.Runtime().StateSnapshot().Session.Projection.ModelRef)
			current, err = a.replaceControllerForSessionOpenLocked(navigationCtx, tab, current, service, ref, targetModel, workspace, wantedNavigation)
			if err != nil {
				return HistoryPage{}, err
			}
		}
	}
	if err := a.commitCanonicalSessionBinding(tab, current, ref, workspace, wantedNavigation); err != nil {
		return HistoryPage{}, err
	}
	a.setTabReadOnly(tab.ID, false)
	a.invalidatePromptHistoryCache()
	a.notifyTabRuntimeRebuilt(tab)
	if !includeHistory {
		return HistoryPage{Messages: []HistoryMessage{}}, nil
	}
	return historyPageFromMessagesForTab(tab, current, current.History(), 0, limit), nil
}

// replaceControllerForSessionOpenLocked prepares an Agent for the target session's
// recorded model before publishing it to the tab. The caller holds
// runtimeRebuildMu and tab.turnStartMu, so the source remains usable until the
// target model, writer, and event projection have all been validated.
func (a *App) replaceControllerForSessionOpenLocked(ctx context.Context, tab *WorkspaceTab, current control.SessionAPI, service *session.Service, ref session.SessionRef, targetModel string, workspace workspacestate.Workspace, navigationSequence ...uint64) (control.SessionAPI, error) {
	if tab == nil || service == nil {
		return nil, fmt.Errorf("session runtime changed while opening session")
	}
	transition, err := a.reserveSessionRuntimePath(tab, sessionRoute(ref.SessionID))
	if err != nil {
		return nil, userFacingSessionLeaseError("", err)
	}
	committed := false
	// boot retains its context for MCP and other controller-owned work. Relay
	// navigation cancellation only until publication, then keep the app lifetime.
	controllerCtx, cancelController := context.WithCancel(a.bootContext())
	stopNavigationCancellation := context.AfterFunc(ctx, cancelController)
	defer func() {
		stopNavigationCancellation()
		if !committed {
			cancelController()
			a.rollbackSessionRuntimePath(transition)
		}
	}()
	prepared, err := a.prepareSessionOpenEnvironment(tab, workspace)
	if err != nil {
		return nil, err
	}
	defer func() { a.finishSessionOpenEnvironment(prepared, committed) }()
	snap, cfg, root, sharedHost := prepared.snapshot, prepared.config, workspace.Root, prepared.host
	if targetModel == "" {
		targetModel, _, _ = cfg.ResolveDesktopNewSessionModel()
	}
	extensionGeneration := a.currentExtensionGeneration()
	buildOptions := a.sessionOpenBootOptions(tab, snap, cfg, service, sharedHost, root, targetModel)
	requestedModel := targetModel
	candidate, targetModel, fallbackUsed, err := a.buildSessionOpenControllerCandidate(controllerCtx, extensionGeneration, cfg, buildOptions)
	if err != nil {
		return nil, err
	}
	discard := true
	defer func() {
		if discard {
			candidate.Close()
		}
	}()
	candidateIdentity, ok := candidate.(control.IdentityLifecycle)
	if !ok || !candidateIdentity.UsesExclusiveSession() {
		return nil, fmt.Errorf("replacement session identity protocol is unavailable")
	}
	if _, err := candidateIdentity.OpenSession(ctx, ref); err != nil {
		return nil, err
	}
	if fallbackUsed {
		if err := service.SetModel(ctx, ref, targetModel, cfg.ModelSelectionIdentity(targetModel)); err != nil {
			return nil, err
		}
		a.noticeForTab(tab.ID, fmt.Sprintf("model %q is no longer available; switched to %s", requestedModel, targetModel))
	}
	a.bindControllerDisplayRecorder(candidate)
	_, fallbackPreset := desktopNewSessionDefaults(snap.scope, root)
	runtime := prepareCanonicalControllerRuntime(candidate, snap, a.sessionPresets.restore(ref.SessionID, fallbackPreset))

	confirmed, err := a.canonicalSessionWorkspace(ctx, ref)
	if err != nil {
		return nil, err
	}
	if confirmed.ID != workspace.ID || !sameDesktopPath(confirmed.Root, root) {
		return nil, errSessionWorkspaceConflict
	}
	var terminalSessions []*terminalSession
	a.mu.Lock()
	if err := a.authorizeSessionOpenPublicationLocked(tab, current, candidate, navigationSequence); err != nil {
		a.mu.Unlock()
		return nil, err
	}
	if !stopNavigationCancellation() || ctx.Err() != nil {
		a.mu.Unlock()
		return nil, context.Canceled
	}
	oldSink := tab.sink
	if !a.commitCanonicalRuntimeTransitionLocked(tab, transition, prepared.preserveSource) {
		a.mu.Unlock()
		return nil, fmt.Errorf("tab runtime changed while opening session")
	}
	if prepared.workspaceChanged && a.terminals != nil {
		terminalSessions = a.terminals.detachForTab(tab.ID)
	}
	applyCanonicalWorkspaceLocked(tab, workspace, prepared.workspaceChanged)
	tab.SharedHostKey = snap.sharedHostKey
	tab.Ctrl = candidate
	tab.sink = snap.sink
	tab.adoptDisplayState(&tabDisplayState{})
	tab.ActivityStatus = ""
	tab.replaceTelemetry(loadTelemetryFor(sessionRoute(ref.SessionID)), sessionRuntimeKey(sessionRoute(ref.SessionID)))
	setTabSessionIdentity(tab, sessionRoute(ref.SessionID))
	tab.model = targetModel
	tab.Label = candidate.Label()
	applyNormalizedRuntimeToTabLocked(tab, runtime)
	tab.Ready = true
	clearTabStartupError(tab)
	if prepared.preserveSource {
		a.newSessionRuntimeLocked(tab, transition.targetKey)
	}
	tab.sink.setBinding(tab.ID, a, tab.SessionGeneration)
	tab.sink.setContext(a.ctx)
	a.bindSessionRuntimeKeyLocked(tab, tab.currentSessionIdentity())
	a.supersedeTabBuildLocked(tab)
	a.saveTabsLocked()
	epoch := a.advanceSessionRuntimeEpochLocked(tab)
	committed = true
	a.mu.Unlock()

	if !prepared.preserveSource {
		fenceCanonicalNavigationSink(oldSink)
		retireReplacedController(current, candidate)
	}
	if prepared.workspaceChanged {
		a.finishCanonicalWorkspaceMove(tab.ID, terminalSessions)
	}
	discard = false
	a.notifyTabRuntimeRebuiltAtEpoch(tab, epoch)
	return candidate, nil
}

// The caller holds App.mu so intent, surface identity, and replacement guards
// are checked against the same state immediately before controller publication.
func (a *App) authorizeSessionOpenPublicationLocked(tab *WorkspaceTab, current, candidate control.SessionAPI, navigation []uint64) error {
	if len(navigation) > 0 && navigation[0] != 0 && a.desktopSessions.navigationSeq.Load() != navigation[0] {
		return errSessionNavigationSuperseded
	}
	if tab.removed || a.tabs[tab.ID] != tab || tab.Ctrl != current {
		return fmt.Errorf("tab runtime changed while opening session")
	}
	return a.authorizeTabReplacementLocked(tab, candidate, "opening session", "session-open")
}
