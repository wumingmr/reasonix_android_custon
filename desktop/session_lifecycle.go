package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/session"
)

func (a *App) archiveSessionRefsWithOperation(refs []session.SessionRef, operationID string, dependencies ...string) error {
	return a.archiveSessionRefsWithOperationConditional(refs, operationID, nil, dependencies...)
}

// archiveSessionRefsWithOperationConditional runs verify after runtime and
// filesystem maintenance ownership has been acquired, while session removal is
// still serialized. It is used by maintenance jobs whose read decision must be
// fenced from a concurrent title/content/runtime mutation.
//
// Archiving the last visible session leaves the surface empty on purpose: the
// frontend lands on the workspace draft instead of a replacement blank session.
func (a *App) archiveSessionRefsWithOperationConditional(refs []session.SessionRef, operationID string, verify func(context.Context, workspacestate.State) error, dependencies ...string) error {
	a.sessionRemovalMu.Lock()
	defer a.sessionRemovalMu.Unlock()
	cleanup := archivedRuntimeCleanup{app: a}
	defer cleanup.finish()
	return a.archiveSessionRefsRemovalHeld(refs, operationID, verify, &cleanup, dependencies...)
}

// archiveSessionRefsRemovalHeld commits and detaches only. The caller holds
// sessionRemovalMu and must finish cleanup after releasing title/index locks.
func (a *App) archiveSessionRefsRemovalHeld(refs []session.SessionRef, operationID string, verify func(context.Context, workspacestate.State) error, cleanup *archivedRuntimeCleanup, dependencies ...string) error {
	ctx := a.bootContext()
	service := a.desktopSessionService("")
	unique := map[string]session.SessionRef{}
	for _, ref := range refs {
		if err := validateLocalSessionRef(ref); err != nil {
			return err
		}
		a.cancelAISessionTitle((SessionTarget{SessionRef: ref}).key())
		unique[ref.SessionID] = ref
	}
	if len(unique) == 0 {
		return errors.New("no sessions to archive")
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	legacyTargets := map[string]bool{}
	for _, dependency := range dependencies {
		if mapping := state.PendingOperations[dependency].Mapping; mapping != nil {
			legacyTargets[sessionRuntimeKey(mapping.Path)] = true
		}
	}
	removed, err := a.idleArchiveRuntimes(ids, legacyTargets)
	if err != nil {
		return err
	}
	guards := []func(){}
	staged := map[string]string{}
	for _, dependency := range dependencies {
		op, ok := state.PendingOperations[dependency]
		if !ok || op.Kind != "archive-import" || op.Phase != "content_ready" {
			return workspacestate.ErrMutationConflict
		}
		for _, id := range op.SessionIDs {
			staged[id] = op.WorkspaceID
		}
	}
	defer func() {
		for _, release := range slices.Backward(guards) {
			release()
		}
	}()
	for _, id := range ids {
		ref := unique[id]
		if err := a.validateConditionalArchiveWorkspace(ctx, state, ref, staged[id], verify != nil); err != nil {
			return err
		}
		if runtime, live := service.Runtime(ref); live {
			phase := runtime.StateSnapshot().Phase
			if phase != session.RuntimeIdle && phase != session.RuntimeRecoveryRequired {
				return errTopicHasActiveWork
			}
		} else {
			guard, err := session.NewFilesystemPersistence(a.desktopSessions.root).AcquireMaintenance(id)
			if err != nil {
				return userFacingSessionLeaseError("", err)
			}
			guards = append(guards, guard)
		}
		if err := requireArchivableSession(ctx, service, ref); err != nil {
			return err
		}
	}
	state, err = a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	op := workspacestate.Operation{ID: operationID, Kind: "archive", Lifecycle: workspacestate.Archived, SessionIDs: ids, ExpectedGeneration: state.Generation, Dependencies: dependencies}
	if err := a.beginConditionalArchiveOperation(ctx, state, op, verify); err != nil {
		return err
	}
	if err := a.workspaceRegistry().PrepareOperationContent(ctx, op.ID, ids, nil, nil); err != nil {
		return err
	}
	a.lifecycleCheckpoint("before-archive-commit")
	if err := a.workspaceRegistry().CommitOperation(ctx, op.ID); err != nil {
		return err
	}
	a.detachArchivedRuntimeBindings(removed)
	cleanup.removed = append(cleanup.removed, removed...)
	for _, ref := range unique {
		cleanup.refs = append(cleanup.refs, ref)
	}
	return nil
}

func (a *App) validateConditionalArchiveWorkspace(ctx context.Context, state workspacestate.State, ref session.SessionRef, stagedWorkspaceID string, maintenance bool) error {
	if stagedWorkspaceID != "" {
		return a.validateDesktopWorkspaceMembership(ctx, stagedWorkspaceID, ref)
	}
	if !maintenance {
		_, err := a.canonicalSessionWorkspace(ctx, ref)
		return err
	}
	// A final content/lifecycle CAS lets maintenance archive proven-empty
	// sessions even when immutable and registry workspace ownership disagree.
	if !conditionalArchiveRegistryHasSingleActiveOwner(state, ref.SessionID) {
		return workspacestate.ErrMutationConflict
	}
	return nil
}

func conditionalArchiveRegistryHasSingleActiveOwner(state workspacestate.State, sessionID string) bool {
	if state.SessionStates[sessionID].Lifecycle != workspacestate.Active {
		return false
	}
	owners := 0
	for _, workspace := range state.Workspaces {
		if slices.Contains(workspace.SessionIDs, sessionID) {
			owners++
		}
	}
	return owners == 1
}

func (a *App) beginConditionalArchiveOperation(ctx context.Context, state workspacestate.State, op workspacestate.Operation, verify func(context.Context, workspacestate.State) error) error {
	if verify != nil {
		if err := verify(ctx, state); err != nil {
			return err
		}
	}
	return a.workspaceRegistry().BeginOperation(ctx, op)
}

// Called only after durable commit, with runtime mutation admission held.
func (a *App) finishArchivedRuntimeBindings(removed []removedSessionRuntime) {
	a.detachArchivedRuntimeBindings(removed)
	a.finalizeRemovedTopicRuntimes(removed)
	a.closeRemainingRemovedSessionRuntimesAdmissionHeld(removed, map[control.SessionAPI]bool{})
}

func (a *App) detachArchivedRuntimeBindings(removed []removedSessionRuntime) {
	a.mu.Lock()
	for _, item := range removed {
		tab := item.tab
		if tab.Ctrl != item.ctrl {
			continue
		}
		a.markTabRemovedLocked(tab)
		stopTabAutosave(tab)
		a.releaseSessionRuntimeLocked(tab)
		a.unregisterDetachedRuntimeLocked(tab)
		delete(a.tabs, tab.ID)
		a.removeTabOrderLocked(tab.ID)
		if a.activeTabID == tab.ID {
			a.activeTabID = ""
		}
	}
	if a.activeTabID == "" && len(a.tabOrder) > 0 {
		a.activeTabID = a.tabOrder[0]
	}
	var dir, activeID string
	var entries []desktopTabEntry
	var version uint64
	if len(removed) > 0 {
		dir, entries, activeID, version = a.saveTabsCollectLocked()
	}
	a.mu.Unlock()
	if len(removed) > 0 {
		a.saveTabsWrite(dir, entries, activeID, version)
	}
}

func (a *App) archiveCompatibleTopic(topicID string) error {
	release, ok := a.tryLockRuntimeMutation("archive topic")
	if !ok {
		return errTopicArchiveBusy
	}
	defer release()
	return a.archiveCompatibleTopicAdmissionHeld(topicID, "")
}

func (a *App) archiveCompatibleTopicAdmissionHeld(topicID, operationID string) error {
	return a.archiveCompatibleTopicWithCleanupAdmissionHeld(topicID, operationID, nil)
}

// A non-nil cleanup means the caller already holds sessionRemovalMu, followed
// by the title/index locks, for a confirmed session-backed topic removal.
func (a *App) archiveCompatibleTopicWithCleanupAdmissionHeld(topicID, operationID string, cleanup *archivedRuntimeCleanup) error {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return fmt.Errorf("topicID is required")
	}
	if a.topicHasActiveRuntimeWork(topicID) {
		return errTopicHasActiveWork
	}
	refs := map[string]session.SessionRef{}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return err
	}
	for id, presentation := range state.Presentation {
		if presentation.TopicID == topicID {
			refs[id] = session.SessionRef{HostID: localDesktopHostID, SessionID: id}
		}
	}
	if id, ok := strings.CutPrefix(topicID, "canonical-"); ok {
		if _, registered := state.SessionStates[id]; registered {
			refs[id] = session.SessionRef{HostID: localDesktopHostID, SessionID: id}
		}
	}
	a.mu.RLock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab != nil && tab.TopicID == topicID && tab.SessionID != "" {
			refs[tab.SessionID] = session.SessionRef{HostID: localDesktopHostID, SessionID: tab.SessionID}
		}
	}
	a.mu.RUnlock()
	owners := a.captureTopicRuntimeBindings(topicID)
	if err := a.snapshotTopicRuntimeBindings(owners); err != nil {
		return err
	}
	// Snapshot first: a legacy runtime may publish its first durable file here.
	// Only a topic with neither canonical identities nor legacy content may use
	// the metadata-only removal path.
	targets, err := a.topicTrashTargets(topicID)
	if err != nil {
		return err
	}
	if err := a.validateCompatibleTopicOwner(state, topicID, len(targets) > 0); err != nil {
		return err
	}
	if len(refs) == 0 && len(targets) == 0 {
		return a.removeEmptyCompatibleTopicAdmissionHeld(topicID, cleanup)
	}
	// Originals stay in place, so retain existing leases and acquire only cold
	// sources. The importer recognizes these same-process owners when freezing.
	localOwners := topicArchiveLeaseOwners(owners)
	leases := []*agent.SessionLease{}
	defer func() {
		for _, lease := range leases {
			lease.Release()
		}
	}()
	for _, target := range targets {
		if localOwners[sessionRuntimeKey(target.sessionPath)] != nil {
			continue
		}
		lease, err := agent.TryAcquireSessionLease(target.sessionPath)
		if err != nil {
			if errors.Is(err, agent.ErrSessionLeaseHeld) {
				return errSessionBusyElsewhere
			}
			return err
		}
		leases = append(leases, lease)
	}
	dependencies := []string{}
	for _, target := range targets {
		ref, dependency, err := a.stageArchiveSource(a.bootContext(), target.sessionPath)
		if err != nil {
			return err
		}
		if dependency != "" {
			dependencies = append(dependencies, dependency)
		}
		refs[ref.SessionID] = ref
	}
	list := make([]session.SessionRef, 0, len(refs))
	for _, ref := range refs {
		list = append(list, ref)
	}
	if operationID == "" {
		operationID = "archive-" + newTabID()
	}
	if cleanup != nil {
		err = a.archiveSessionRefsRemovalHeld(list, operationID, nil, cleanup, dependencies...)
	} else {
		err = a.archiveSessionRefsWithOperation(list, operationID, dependencies...)
	}
	if err != nil {
		return err
	}
	for _, lease := range leases {
		lease.Release()
	}
	leases = nil
	a.emitProjectTreeChanged()
	return nil
}

func (a *App) removeEmptyCompatibleTopicAdmissionHeld(topicID string, cleanup *archivedRuntimeCleanup) error {
	// A confirmed session removal must not silently turn into placeholder
	// removal (which also acquires the removal/title locks).
	if cleanup != nil {
		return workspacestate.ErrMutationConflict
	}
	return a.removeCompatiblePlaceholderAdmissionHeld(topicID)
}

func (a *App) stageArchiveSource(ctx context.Context, path string) (session.SessionRef, string, error) {
	if ref, found, err := a.legacyCanonicalRef(ctx, path); found || err != nil {
		return ref, "", err
	}
	meta, _, err := agent.LoadBranchMeta(path)
	if err != nil {
		return session.SessionRef{}, "", err
	}
	scope, root := "global", ""
	if meta.WorkspaceRoot != "" && !a.isGlobalWorkspacePath(ctx, meta.WorkspaceRoot) {
		scope, root = "project", meta.WorkspaceRoot
	}
	workspaceID, err := a.ensureDesktopWorkspace(ctx, scope, root)
	if err != nil {
		return session.SessionRef{}, "", err
	}
	fingerprint, err := desktopSourceFingerprint(path)
	if err != nil {
		return session.SessionRef{}, "", err
	}
	if err := a.migrateLegacySession(ctx, path, desktopMigrationSource{scope: scope, workspaceRoot: root, deferArchive: true}, workspaceID); err != nil {
		return session.SessionRef{}, "", err
	}
	opID := "archive-import-" + desktopSourceKey(path, "") + "-" + fingerprint
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return session.SessionRef{}, "", err
	}
	op, exists := state.PendingOperations[opID]
	if !exists || op.Phase != "content_ready" || len(op.SessionIDs) != 1 {
		return session.SessionRef{}, "", workspacestate.ErrMutationConflict
	}
	return session.SessionRef{HostID: localDesktopHostID, SessionID: op.SessionIDs[0]}, opID, nil
}

func (a *App) restoreCanonicalSession(ctx context.Context, ref session.SessionRef, operationID string, recoveryIDs ...string) (SessionRestoreResult, error) {
	if err := validateLocalSessionRef(ref); err != nil {
		return SessionRestoreResult{}, err
	}
	workspace, err := a.canonicalSessionWorkspace(ctx, ref)
	if errors.Is(err, errSessionWorkspaceConflict) && len(recoveryIDs) == 1 {
		info, readErr := a.desktopSessionService("").Query().Stat(ctx, ref)
		if readErr != nil {
			return SessionRestoreResult{}, readErr
		}
		if info.CWD == "" || info.Origin == "" {
			return SessionRestoreResult{}, errSessionWorkspaceConflict
		}
		scope, root := "project", info.CWD
		if a.isGlobalWorkspacePath(ctx, root) {
			scope, root = "global", ""
		}
		workspaceID, ensureErr := a.ensureDesktopWorkspace(ctx, scope, root)
		if ensureErr != nil {
			return SessionRestoreResult{}, ensureErr
		}
		state, loadErr := a.workspaceRegistry().Load(ctx)
		if loadErr != nil {
			return SessionRestoreResult{}, loadErr
		}
		workspace, err = state.Workspaces[workspaceID], nil
	}
	if err != nil {
		return SessionRestoreResult{}, err
	}
	if err := requireArchivableSession(ctx, a.desktopSessionService(""), ref); err != nil {
		return SessionRestoreResult{}, err
	}
	if operationID == "" {
		operationID = "restore-" + newTabID()
	}
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	op := workspacestate.Operation{ID: operationID, Kind: "restore", WorkspaceID: workspace.ID, Lifecycle: workspacestate.Active, SessionIDs: []string{ref.SessionID}, ExpectedGeneration: state.Generation}
	if len(recoveryIDs) == 1 {
		op.RecoveryEntryID = recoveryIDs[0]
	}
	if err := a.workspaceRegistry().BeginOperation(ctx, op); err != nil {
		return SessionRestoreResult{}, err
	}
	if err := a.workspaceRegistry().PrepareOperationContent(ctx, op.ID, op.SessionIDs, nil, nil); err != nil {
		return SessionRestoreResult{}, err
	}
	if err := a.workspaceRegistry().CommitOperation(ctx, op.ID); err != nil {
		return SessionRestoreResult{}, err
	}
	a.markLegacyCleanupSessionRestored(ref.SessionID)
	state, err = a.workspaceRegistry().Load(ctx)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	a.emitProjectTreeChanged()
	return SessionRestoreResult{Session: ref, WorkspaceID: workspace.ID, Generation: state.PendingOperations[op.ID].ResultGeneration}, nil
}

type SessionRestoreResult struct {
	Session     session.SessionRef `json:"session"`
	WorkspaceID string             `json:"workspaceId"`
	Generation  uint64             `json:"generation"`
}

func (a *App) idleArchiveRuntimes(ids []string, legacyTargets map[string]bool) ([]removedSessionRuntime, error) {
	a.mu.RLock()
	removed := []removedSessionRuntime{}
	for _, tab := range a.runtimeTabsLocked() {
		if tab == nil || (!containsDesktopString(ids, tab.SessionID) && !legacyTargets[sessionRuntimeKey(tab.currentSessionPath())]) {
			continue
		}
		item := removedRuntimeFromTab(tab, tabRuntimeSessionDir(tab), tab.currentSessionPath())
		item.failedStartup = a.suppressTabStartupRestoreLocked(tab)
		removed = append(removed, item)
	}
	a.mu.RUnlock()
	for _, item := range removed {
		if item.ctrl != nil && controllerHasActiveRuntimeWork(item.ctrl) {
			return nil, errTopicHasActiveWork
		}
	}
	if err := a.snapshotTopicRuntimeBindings(removed); err != nil {
		return nil, err
	}
	return removed, nil
}

// requireArchivableSession proves the session exists. Moving a session in or
// out of the archive never reads its content, so a damaged store stays movable:
// archiving is how a user sets aside a session that no longer opens.
func requireArchivableSession(ctx context.Context, service *session.Service, ref session.SessionRef) error {
	_, err := service.Query().Snapshot(ctx, ref)
	if errors.Is(err, session.ErrDamagedStore) {
		return nil
	}
	return err
}
