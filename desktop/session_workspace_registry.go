package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/session"
)

type freshSessionCreator interface {
	BindFreshSession(context.Context, string) (session.SessionRef, error)
}

// desktopSessionState groups the Desktop-only persistence and navigation
// authority so App does not grow a second set of independent scalar owners.
type desktopSessionState struct {
	readSnapshots                 readSnapshotStore
	layoutWrites                  tabLayoutWriter
	organizations                 organizationReadCache
	beforeMigrationRegistryCommit func() error
	root                          string
	workspaceState                *workspacestate.Store
	navigationSeq                 atomic.Uint64
	navigationMu                  sync.Mutex
	navigationGeneration          uint64
	navigationCancel              context.CancelFunc
	pruneBlockedPersistence       atomic.Uint64
	pendingCreateRecovered        atomic.Uint64
}

func (a *App) beginSessionNavigationContext(navigation ...uint64) (context.Context, func()) {
	base := a.bootContext()
	a.desktopSessions.navigationMu.Lock()
	// Admission and cancellation share this lock: a delayed request must not
	// cancel a newer intent or register after shutdown's cancellation sweep.
	var rejected error
	if a.shuttingDown.Load() {
		rejected = context.Canceled
	} else if len(navigation) > 0 && navigation[0] != 0 && a.desktopSessions.navigationSeq.Load() != navigation[0] {
		rejected = errSessionNavigationSuperseded
	}
	if rejected != nil {
		a.desktopSessions.navigationMu.Unlock()
		ctx, cancel := context.WithCancelCause(base)
		cancel(rejected)
		return ctx, func() {}
	}
	if a.desktopSessions.navigationCancel != nil {
		a.desktopSessions.navigationCancel()
	}
	a.desktopSessions.navigationGeneration++
	generation := a.desktopSessions.navigationGeneration
	ctx, cancel := context.WithCancel(base)
	a.desktopSessions.navigationCancel = cancel
	a.desktopSessions.navigationMu.Unlock()
	return ctx, func() {
		cancel()
		a.desktopSessions.navigationMu.Lock()
		if a.desktopSessions.navigationGeneration == generation {
			a.desktopSessions.navigationCancel = nil
		}
		a.desktopSessions.navigationMu.Unlock()
	}
}

func (a *App) cancelSessionNavigation() {
	if a == nil {
		return
	}
	a.desktopSessions.navigationMu.Lock()
	a.desktopSessions.navigationGeneration++
	if a.desktopSessions.navigationCancel != nil {
		a.desktopSessions.navigationCancel()
		a.desktopSessions.navigationCancel = nil
	}
	a.desktopSessions.navigationMu.Unlock()
}

func newDesktopSessionState() desktopSessionState {
	return desktopSessionState{
		root:           config.DesktopSessionStoreDir(),
		workspaceState: newDesktopWorkspaceStore(),
	}
}

func (a *App) initializeDesktopSessionRoot() {
	a.sessionServicesMu.Lock()
	defer a.sessionServicesMu.Unlock()
	if len(a.sessionServices) == 0 {
		a.desktopSessions.root = config.DesktopSessionStoreDir()
	}
}

func restoredWorkspaceID(entry desktopTabEntry) string {
	if id := strings.TrimSpace(entry.WorkspaceID); id != "" {
		return id
	}
	return desktopWorkspaceID(entry.Scope, entry.WorkspaceRoot)
}

func desktopWorkspaceID(scope, workspaceRoot string) string {
	if strings.TrimSpace(scope) != "project" {
		return workspacestate.GlobalWorkspaceID
	}
	root := canonicalRuntimeRoot(workspaceRoot)
	digest := sha256.Sum256([]byte(root))
	return "project-" + hex.EncodeToString(digest[:12])
}

func desktopWorkspaceOwnerID(state workspacestate.State, scope, workspaceRoot string) string {
	id := desktopWorkspaceID(scope, workspaceRoot)
	if strings.TrimSpace(scope) != "project" {
		return id
	}
	if persisted, ok, err := workspacestate.ResolveWorkspaceID(state, workspaceRoot); err == nil && ok {
		return persisted
	}
	return id
}

func (a *App) resolveDesktopWorkspaceID(ctx context.Context, scope, workspaceRoot string) (string, error) {
	id := desktopWorkspaceID(scope, workspaceRoot)
	if strings.TrimSpace(scope) != "project" {
		return id, nil
	}
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return "", err
	}
	persisted, ok, err := workspacestate.ResolveWorkspaceID(state, workspaceRoot)
	if err != nil {
		return "", err
	}
	if ok {
		return persisted, nil
	}
	return id, nil
}

func desktopWorkspaceRoot(scope, workspaceRoot string) string {
	if strings.TrimSpace(scope) != "project" {
		return globalWorkspaceRoot()
	}
	return filepath.Clean(strings.TrimSpace(workspaceRoot))
}

// isGlobalWorkspacePath reports whether root is Global's directory: the one
// derived from the data directory now, or one Global was rebound away from.
func isGlobalWorkspacePath(global workspacestate.Workspace, root string) bool {
	if sameDesktopPath(root, globalWorkspaceRoot()) || (global.Root != "" && sameDesktopPath(root, global.Root)) {
		return true
	}
	return slices.ContainsFunc(global.FormerRoots, func(former string) bool { return sameDesktopPath(root, former) })
}

func (a *App) isGlobalWorkspacePath(ctx context.Context, root string) bool {
	if sameDesktopPath(root, globalWorkspaceRoot()) {
		return true
	}
	state, err := a.workspaceRegistry().Load(ctx)
	return err == nil && isGlobalWorkspacePath(state.Workspaces[workspacestate.GlobalWorkspaceID], root)
}

func (a *App) workspaceRegistry() *workspacestate.Store {
	if a == nil {
		return nil
	}
	a.sessionServicesMu.Lock()
	defer a.sessionServicesMu.Unlock()
	if a.desktopSessions.workspaceState == nil {
		a.desktopSessions.workspaceState = newDesktopWorkspaceStore()
	}
	return a.desktopSessions.workspaceState
}

func newDesktopWorkspaceStore() *workspacestate.Store {
	path := config.DesktopWorkspaceStatePath()
	return workspacestate.NewStore(path, func(ctx context.Context) error { return backupDesktopUpgradeMetadataAt(ctx, path) })
}

func (a *App) ensureDesktopWorkspace(ctx context.Context, scope, workspaceRoot string) (string, error) {
	store := a.workspaceRegistry()
	if store == nil || store.Path() == "" || store.Path() == "." {
		return "", errors.New("desktop workspace registry is unavailable")
	}
	id := desktopWorkspaceID(scope, workspaceRoot)
	title := globalProjectTitle()
	if strings.TrimSpace(scope) == "project" {
		title = workspaceName(workspaceRoot)
	}
	resolvedID, err := store.EnsureWorkspaceResolved(ctx, workspacestate.Workspace{
		ID: id, Root: desktopWorkspaceRoot(scope, workspaceRoot), Title: title, Visible: true,
	})
	return resolvedID, err
}

func (a *App) bindFreshDesktopSession(ctx context.Context, scope, workspaceRoot string, creator freshSessionCreator) (session.SessionRef, string, error) {
	return a.bindFreshDesktopSessionWithIDs(ctx, scope, workspaceRoot, creator, "", "")
}

func (a *App) bindFreshDesktopSessionWithIDs(ctx context.Context, scope, workspaceRoot string, creator freshSessionCreator, sessionID, operationID string) (session.SessionRef, string, error) {
	workspaceID, err := a.ensureDesktopWorkspace(ctx, scope, workspaceRoot)
	if err != nil {
		return session.SessionRef{}, "", err
	}
	if sessionID = strings.TrimSpace(sessionID); sessionID == "" {
		sessionID = "desktop-" + strings.TrimPrefix(newTabID(), "tab_")
	}
	if operationID = strings.TrimSpace(operationID); operationID == "" {
		operationID = "create-" + strings.TrimPrefix(newTabID(), "tab_")
	}
	store := a.workspaceRegistry()
	if err := store.BeginCreate(ctx, workspacestate.PendingCreate{OperationID: operationID, WorkspaceID: workspaceID, SessionID: sessionID}); err != nil {
		return session.SessionRef{}, "", err
	}
	options := session.CreateOptions{SessionID: sessionID, CWD: desktopWorkspaceRoot(scope, workspaceRoot), Origin: session.SessionOriginNew}
	var ref session.SessionRef
	if headerCreator, ok := creator.(interface {
		BindFreshSessionWithOptions(context.Context, session.CreateOptions) (session.SessionRef, error)
	}); ok {
		ref, err = headerCreator.BindFreshSessionWithOptions(ctx, options)
	} else {
		ref, err = creator.BindFreshSession(ctx, sessionID)
	}
	if err != nil {
		return session.SessionRef{}, workspaceID, err
	}
	if err := a.validateDesktopWorkspaceMembership(ctx, workspaceID, ref); err != nil {
		return ref, workspaceID, err
	}
	if err := store.AttachSession(ctx, operationID, workspaceID, ref.SessionID, ""); err != nil {
		return ref, workspaceID, err
	}
	return ref, workspaceID, nil
}

func (a *App) attachDesktopSession(ctx context.Context, scope, workspaceRoot string, ref session.SessionRef) (string, error) {
	workspaceID, err := a.ensureDesktopWorkspace(ctx, scope, workspaceRoot)
	if err != nil {
		return "", err
	}
	if err := a.validateDesktopWorkspaceMembership(ctx, workspaceID, ref); err != nil {
		return "", err
	}
	if err := a.workspaceRegistry().AttachSession(ctx, "", workspaceID, ref.SessionID, ""); err != nil {
		return "", err
	}
	if runtime, ok := a.desktopSessionService("").Runtime(ref); ok {
		if source := runtime.Session().Manifest().Source; source != nil && source.Path != "" {
			// Adoption is durable; opening a tab must not re-hash a refreshed
			// source. The import path owns source validation and registry writes.
			state, stateErr := a.workspaceRegistry().Load(ctx)
			adopted := false
			if stateErr == nil {
				for _, mapping := range state.SourceMappings {
					if mapping.SessionID == ref.SessionID && mapping.WorkspaceID == workspaceID &&
						sessionRuntimeKey(mapping.Path) == sessionRuntimeKey(source.Path) {
						adopted = true
						break
					}
				}
			}
			if !adopted {
				if fingerprint, err := desktopSourceFingerprint(source.Path); err == nil {
					if err := a.recordDesktopSource(ctx, source.Path, "legacy", fingerprint, ref.SessionID, workspaceID); err != nil &&
						!a.forkSourceOwnedElsewhere(ctx, err, source.Path, ref) {
						return "", err
					}
				}
			}
		}
	}
	return workspaceID, nil
}

// forkSourceOwnedElsewhere reports whether a rejected source registration is
// a fork naming a source another session already owns; only forks may skip.
func (a *App) forkSourceOwnedElsewhere(ctx context.Context, recordErr error, sourcePath string, ref session.SessionRef) bool {
	if !errors.Is(recordErr, workspacestate.ErrMutationConflict) {
		return false
	}
	info, err := a.desktopSessionService("").Query().Stat(ctx, ref)
	if err != nil || info.Origin != session.SessionOriginFork {
		return false
	}
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return false
	}
	owner, ok := state.SourceMappings[desktopSourceKey(sourcePath, "")]
	return ok && owner.SessionID != ref.SessionID
}

func (a *App) validateDesktopWorkspaceMembership(ctx context.Context, workspaceID string, ref session.SessionRef) error {
	if err := validateLocalSessionRef(ref); err != nil {
		return err
	}
	snapshot, err := a.workspaceRegistry().VerifySnapshot(ctx)
	if err != nil {
		return err
	}
	workspace, ok := snapshot.WorkspaceMetadata(strings.TrimSpace(workspaceID))
	if !ok {
		return workspacestate.ErrWorkspaceNotFound
	}
	info, err := a.desktopSessionService("").Query().Stat(ctx, ref)
	if err != nil {
		return err
	}
	if info.Origin == "" || strings.TrimSpace(info.CWD) == "" {
		return fmt.Errorf("desktop session %q has no immutable workspace header", ref.SessionID)
	}
	same, identityErr := sameDesktopPathStrict(info.CWD, workspace.Root)
	if identityErr != nil {
		return fmt.Errorf("resolve desktop session workspace identity: %w", identityErr)
	}
	if !same {
		return errSessionWorkspaceConflict
	}
	return nil
}

func (a *App) attachForkedDesktopSession(ctx context.Context, source *WorkspaceTab, childSessionID string) error {
	if source == nil || strings.TrimSpace(childSessionID) == "" {
		return errors.New("desktop fork requires source and child identities")
	}
	workspaceID := strings.TrimSpace(source.SessionWorkspace.ID)
	if workspaceID == "" {
		var err error
		workspaceID, err = a.ensureDesktopWorkspace(ctx, source.Scope, source.WorkspaceRoot)
		if err != nil {
			return err
		}
	}
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	workspace, ok := state.Workspaces[workspaceID]
	if !ok {
		return workspacestate.ErrWorkspaceNotFound
	}
	if err := a.validateDesktopWorkspaceMembership(ctx, workspaceID, session.SessionRef{
		HostID: localDesktopHostID, SessionID: childSessionID,
	}); err != nil {
		return err
	}
	beforeID := ""
	for index, id := range workspace.SessionIDs {
		if id == source.SessionID && index+1 < len(workspace.SessionIDs) {
			beforeID = workspace.SessionIDs[index+1]
			break
		}
	}
	if err := a.workspaceRegistry().AttachSession(ctx, "", workspaceID, childSessionID, beforeID); err != nil {
		return err
	}
	// Canonical forks use workspace membership, not the directory catalog.
	// Invalidate the paged sidebar even if opening the child tab later fails.
	root := ""
	if workspaceID != workspacestate.GlobalWorkspaceID {
		root = workspace.Root
	}
	a.emitProjectTreeChangedV2(a.currentSessionCatalogStatus().Revision, []string{root}, "membership")
	return nil
}

func (a *App) verifyCanonicalTabRegistryBeforePrune(tab *WorkspaceTab) error {
	if tab == nil || strings.TrimSpace(tab.SessionID) == "" {
		return nil
	}
	store := a.workspaceRegistry()
	contained, err := store.Contains(a.bootContext(), tab.SessionID)
	if err != nil {
		return err
	}
	if contained {
		return nil
	}
	_, err = a.attachDesktopSession(a.bootContext(), tab.Scope, tab.WorkspaceRoot, session.SessionRef{
		HostID: localDesktopHostID, SessionID: tab.SessionID,
	})
	return err
}

func (a *App) persistHiddenTabBeforePrune(id string, tab *WorkspaceTab) error {
	if tab != nil && tab.hasActiveRuntimeWork() {
		return nil
	}
	if err := a.snapshotTab(tab); err != nil {
		a.desktopSessions.pruneBlockedPersistence.Add(1)
		slog.Warn("desktop: snapshot before pruning hidden tab failed", "tab", id, "err", err)
		return fmt.Errorf("save current session before switching tabs: %w", err)
	}
	if err := a.saveTabSessionMetaForCurrentSession(tab); err != nil {
		a.desktopSessions.pruneBlockedPersistence.Add(1)
		slog.Warn("desktop: session metadata before pruning hidden tab failed", "tab", id, "err", err)
		return fmt.Errorf("save current session metadata before switching tabs: %w", err)
	}
	if err := a.verifyCanonicalTabRegistryBeforePrune(tab); err != nil {
		a.desktopSessions.pruneBlockedPersistence.Add(1)
		slog.Warn("desktop: canonical registry before pruning hidden tab failed", "tab", id, "err", err)
		return fmt.Errorf("publish current session before switching tabs: %w", err)
	}
	return nil
}

func (a *App) prepareDesktopSessionRotation(ctx context.Context, request control.SessionRotationRequest) (control.SessionRotationPlan, error) {
	if request.SourcePath == "" {
		if err := validateLocalSessionRef(request.Source); err != nil {
			return control.SessionRotationPlan{}, err
		}
	}
	a.mu.RLock()
	var owner *WorkspaceTab
	for _, tab := range a.runtimeTabsLocked() {
		if tab != nil && tab.SessionID == "" && tab.SessionPath != "" && request.Source.SessionID != "" {
			if identity, ok := tab.Ctrl.(control.IdentityLifecycle); ok {
				if ref, bound := identity.SessionRef(); bound && ref == request.Source {
					owner = tab
					request.SourcePath = tab.SessionPath
					break
				}
			}
		}
		if tab != nil && ((request.SourcePath != "" && sameDesktopPath(tab.SessionPath, request.SourcePath)) || (request.Source.SessionID != "" && tab.SessionID == request.Source.SessionID)) {
			owner = tab
			break
		}
	}
	a.mu.RUnlock()
	if owner == nil {
		return control.SessionRotationPlan{}, errors.New("desktop session rotation owner is unavailable")
	}
	workspaceID := strings.TrimSpace(owner.SessionWorkspace.ID)
	if workspaceID == "" {
		var err error
		workspaceID, err = a.ensureDesktopWorkspace(ctx, owner.Scope, owner.WorkspaceRoot)
		if err != nil {
			return control.SessionRotationPlan{}, err
		}
	}
	if request.SourcePath == "" {
		contained, err := a.workspaceRegistry().Contains(ctx, request.Source.SessionID)
		if err != nil {
			return control.SessionRotationPlan{}, err
		}
		if !contained {
			if err := a.validateDesktopWorkspaceMembership(ctx, workspaceID, request.Source); err != nil {
				return control.SessionRotationPlan{}, err
			}
			if err := a.workspaceRegistry().AttachSession(ctx, "", workspaceID, request.Source.SessionID, ""); err != nil {
				return control.SessionRotationPlan{}, err
			}
		}
	}
	sessionID := "desktop-" + strings.TrimPrefix(newTabID(), "tab_")
	operationID := "rotate-" + strings.TrimPrefix(newTabID(), "tab_")
	store := a.workspaceRegistry()
	archiveSource := ""
	if request.Reason == "clear" {
		archiveSource = request.Source.SessionID
	}
	if err := store.BeginCreate(ctx, workspacestate.PendingCreate{OperationID: operationID, WorkspaceID: workspaceID, SessionID: sessionID, ArchiveSource: archiveSource}); err != nil {
		return control.SessionRotationPlan{}, err
	}
	return control.SessionRotationPlan{
		CreateOptions: session.CreateOptions{
			SessionID: sessionID, CWD: desktopWorkspaceRoot(owner.Scope, owner.WorkspaceRoot), Origin: session.SessionOriginNew,
		},
		Commit: func(commitCtx context.Context, ref session.SessionRef) error {
			if ref.SessionID != sessionID {
				return errors.New("desktop session rotation published an unexpected identity")
			}
			if err := a.validateDesktopWorkspaceMembership(commitCtx, workspaceID, ref); err != nil {
				return err
			}
			if err := store.CommitRotation(commitCtx, operationID, workspaceID, sessionID, "", archiveSource); err != nil {
				return err
			}
			return nil
		},
	}, nil
}
