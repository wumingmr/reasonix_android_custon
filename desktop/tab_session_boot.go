package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/plugin"
	"reasonix/internal/session"
)

type tabControllerBootResult struct {
	controller   control.SessionAPI
	ctx          context.Context
	registration *sharedHostMCPRegistration
	model        string
	fallback     bool
	err          error
}

func (a *App) sessionOpenBootOptions(
	tab *WorkspaceTab,
	snap tabRuntimeSnapshot,
	cfg *config.Config,
	service *session.Service,
	sharedHost *plugin.Host,
	root, model string,
) boot.Options {
	return boot.Options{
		Model:                    model,
		RequireKey:               false,
		StatsSource:              "desktop",
		TaskStore:                a.taskStore(),
		OnConfigLoadWarnings:     a.configLoadWarningsHandler(),
		Sink:                     a.desktopControllerSink(snap.sink, cfg.Notifications),
		WorkspaceRoot:            root,
		SessionDir:               sessionDirForSnapshot(snap),
		SessionService:           service,
		EffortOverride:           cloneStringPtr(snap.effort),
		EffortModel:              snap.model,
		SharedHost:               sharedHost,
		BrowserExecutor:          a.browserExecutorForRuntime(tab.ID, snap.sink),
		SharedSkillWatchService:  a.sharedSkillWatchService(),
		MCPHostProfile:           plugin.HostProfileDesktopApps,
		CleanupPendingReconciler: reconcileDesktopCleanupPending,
		SubagentParentLive:       a.subagentParentProbeForBuild(tab),
		SessionRecoveryMeta:      a.tabSessionRecoveryMeta(tab),
		PinnedContextLoader:      pinnedContextLoader(root),
		OnSessionRecovered:       a.handleTabSessionRecovered(tab),
		OnSessionTransition:      a.handleTabSessionTransition(tab),
		BeforeInboxDispatch:      a.beforeInboxDispatch,
		OnSessionTitleChanged:    a.onSessionTitleChanged,
	}
}

func (a *App) bootTabControllerWithModelFallback(
	baseCtx context.Context,
	tab *WorkspaceTab,
	cfg *config.Config,
	sharedHost *plugin.Host,
	options boot.Options,
	extensionGeneration, buildGeneration uint64,
	sessionID, requestedModel string,
) tabControllerBootResult {
	buildCtx, registration := beginSharedHostMCPRegistration(baseCtx, sharedHost)
	controller, err := a.buildTabControllerBootFenced(buildCtx, extensionGeneration, options)
	result := tabControllerBootResult{controller: controller, ctx: buildCtx, registration: registration, model: options.Model, err: err}
	a.mu.RLock()
	draftCreate := tab != nil && strings.TrimSpace(tab.PendingCreateOperationID) != ""
	a.mu.RUnlock()
	if !errors.Is(err, boot.ErrUnknownModel) || strings.TrimSpace(sessionID) == "" || draftCreate {
		return result
	}
	fallbackModel, _, ok := cfg.ResolveDesktopNewSessionModel()
	if !ok || fallbackModel == options.Model {
		return result
	}
	registration.rollback()
	result.ctx, result.registration = beginSharedHostMCPRegistration(baseCtx, sharedHost)
	options.Model = fallbackModel
	result.controller, result.err = a.buildTabControllerBootFenced(result.ctx, extensionGeneration, options)
	if result.err != nil {
		return result
	}
	result.model, result.fallback = fallbackModel, true
	a.noticeForTab(tab.ID, fmt.Sprintf("model %q is no longer available; switched to %s", requestedModel, fallbackModel))
	a.mu.Lock()
	if !a.tabBuildSupersededLocked(tab, buildGeneration) {
		tab.model, tab.Label = fallbackModel, fallbackModel
	}
	a.mu.Unlock()
	return result
}

func (a *App) bindTabCanonicalSession(
	ctx context.Context,
	identity control.IdentityLifecycle,
	cfg *config.Config,
	scope, workspaceRoot, sessionID, legacyPath, model string,
	modelFallback bool,
) (session.SessionRef, string, error) {
	var ref session.SessionRef
	var workspaceID string
	var err error
	switch {
	case strings.TrimSpace(sessionID) != "":
		service := identity.SessionService()
		if service == nil {
			return ref, "", errors.New("v3 session service is unavailable")
		}
		if tab := a.tabForSessionBoot(scope, workspaceRoot, sessionID); tab != nil && strings.HasPrefix(sessionID, "desktop-manual-") {
			a.mu.RLock()
			reserved := tab.PendingCreateOperationID != ""
			a.mu.RUnlock()
			if initializer, ok := identity.(interface {
				InitializeReservedSession(context.Context, session.SessionRef) error
			}); ok && reserved {
				if err := initializer.InitializeReservedSession(ctx, session.SessionRef{HostID: service.HostID(), SessionID: sessionID}); err != nil {
					return ref, "", err
				}
			}
		}
		ref, err = identity.OpenSession(ctx, session.SessionRef{HostID: service.HostID(), SessionID: strings.TrimSpace(sessionID)})
		if errors.Is(err, session.ErrSessionNotFound) {
			operationID := ""
			if tab := a.tabForSessionBoot(scope, workspaceRoot, sessionID); tab != nil {
				operationID = tab.PendingCreateOperationID
			}
			if operationID != "" {
				ref, workspaceID, err = a.bindFreshDesktopSessionWithIDs(ctx, scope, workspaceRoot, identity, sessionID, operationID)
			}
		}
	case strings.TrimSpace(legacyPath) != "":
		ref, err = a.openOrImportDesktopLegacySession(ctx, identity, legacyPath, session.CreateOptions{
			CWD: desktopWorkspaceRoot(scope, workspaceRoot), Origin: session.SessionOriginLegacyImport,
		})
		if errors.Is(err, errUnadoptedLegacySourceMissing) {
			evidence := a.loadSavedTabReconcileEvidence(ctx)
			if evidence.registryErr != nil {
				return ref, "", errors.Join(errLegacySourceRecoveryPending, evidence.registryErr)
			}
			if savedTabHasRecoveryOwner(desktopTabEntry{SessionPath: legacyPath}, evidence) {
				return ref, "", errLegacySourceRecoveryPending
			}
			ref, workspaceID, err = a.bindFreshDesktopSession(ctx, scope, workspaceRoot, identity)
		}
	default:
		ref, workspaceID, err = a.bindFreshDesktopSession(ctx, scope, workspaceRoot, identity)
	}
	if err == nil && workspaceID == "" {
		workspaceID, err = a.attachDesktopSession(ctx, scope, workspaceRoot, ref)
	}
	if err == nil && modelFallback {
		err = identity.SessionService().SetModel(ctx, ref, model, cfg.ModelSelectionIdentity(model))
	}
	return ref, workspaceID, err
}

func (a *App) tabForSessionBoot(scope, workspaceRoot, sessionID string) *WorkspaceTab {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab != nil && tab.Scope == scope && tab.SessionID == sessionID &&
			(scope != "project" || sameProjectRoot(tab.WorkspaceRoot, workspaceRoot)) {
			return tab
		}
	}
	return nil
}

var errUnadoptedLegacySourceMissing = errors.New("unadopted legacy source is missing")
var errLegacySourceRecoveryPending = errors.New("legacy session recovery is pending")

// The Desktop registry owns adoption. Re-freezing a previously imported source
// can generate a different identity after a catalog sidecar refresh, even when
// the historical messages have not changed. Resolve adoption before inspecting
// the source so a retained canonical session also survives source removal.
func (a *App) openOrImportDesktopLegacySession(ctx context.Context, identity control.IdentityLifecycle, path string, options session.CreateOptions) (session.SessionRef, error) {
	if ref, adopted, err := a.legacyCanonicalRef(ctx, path); adopted || err != nil {
		if err != nil {
			return session.SessionRef{}, err
		}
		return identity.OpenSession(ctx, ref)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return session.SessionRef{}, errUnadoptedLegacySourceMissing
		}
		return session.SessionRef{}, err
	}
	if creator, ok := identity.(control.IdentityCreateLifecycle); ok {
		return creator.ContinueLegacySessionWithOptions(ctx, path, "", options)
	}
	return identity.ContinueLegacySession(ctx, path, "")
}

func (a *App) buildSessionOpenControllerCandidate(
	ctx context.Context,
	extensionGeneration uint64,
	cfg *config.Config,
	options boot.Options,
) (control.SessionAPI, string, bool, error) {
	if hook := a.sessionOpenBuildHook; hook != nil {
		hook(ctx)
		if err := ctx.Err(); err != nil {
			return nil, options.Model, false, err
		}
	}
	requestedModel := options.Model
	candidate, err := a.buildTabControllerBootFenced(ctx, extensionGeneration, options)
	if !errors.Is(err, boot.ErrUnknownModel) {
		return candidate, requestedModel, false, err
	}
	fallbackModel, _, ok := cfg.ResolveDesktopNewSessionModel()
	if !ok || fallbackModel == requestedModel {
		return candidate, requestedModel, false, err
	}
	options.Model = fallbackModel
	candidate, err = a.buildTabControllerBootFenced(ctx, extensionGeneration, options)
	return candidate, fallbackModel, err == nil, err
}
