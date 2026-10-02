package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/session"
	"reasonix/internal/store"
)

type savedTabReconcileOutcome string

const (
	restoreTab            savedTabReconcileOutcome = "restore"
	dropStalePresentation savedTabReconcileOutcome = "drop_stale_presentation"
	preserveRecovery      savedTabReconcileOutcome = "preserve_recovery"
	preserveError         savedTabReconcileOutcome = "preserve_error"
)

type savedTabReconcileDecision struct {
	outcome            savedTabReconcileOutcome
	reason             string
	identityKind       string
	waitedForMigration bool
	hadPending         bool
	hadRecoveryOwner   bool
	repairedIdentity   bool
}

type savedTabReconcileEvidence struct {
	registry    workspacestate.State
	registryErr error
}

func (a *App) reconcileTabsBeforeRestore(ctx context.Context, file desktopTabsFile, version uint64) (desktopTabsFile, uint64, bool) {
	original := file
	reconciled, changed := a.reconcileSavedTabs(ctx, file)
	if !a.tabsSnapshotCurrent(version) {
		return file, version, false
	}
	if changed {
		committedVersion, err := a.persistReconciledTabsFile(reconciled, version)
		if committedVersion != 0 {
			version = committedVersion
		}
		if errors.Is(err, errTabsSnapshotChanged) {
			return file, version, false
		}
		if err != nil {
			slog.Warn("desktop_saved_tab_reconcile_persist_failed", "reason", "write_failed")
			fallback := retiredDraftRestoreFallback(original, reconciled)
			return blockSavedTabUnsafeRestore(fallback, "identity_repair_write_failed"), version, a.tabsSnapshotCurrent(version)
		}
	}
	return reconciled, version, a.tabsSnapshotCurrent(version)
}

// Draft retirement is independent of saving tab preferences. Falling back to
// an old operation marker could recreate an abandoned session or reopen the
// retired database. Keep reconciliation's retirement decisions even on a
// write failure, but never start a runtime for an unpersisted identity repair.
func retiredDraftRestoreFallback(original, reconciled desktopTabsFile) desktopTabsFile {
	byID := make(map[string]desktopTabEntry, len(reconciled.Tabs))
	for _, entry := range reconciled.Tabs {
		byID[entry.ID] = entry
	}
	kept := make([]desktopTabEntry, 0, len(original.Tabs))
	removed := map[string]bool{}
	for _, entry := range original.Tabs {
		if strings.HasPrefix(entry.CreateOperationID, "draft-op-") {
			next, exists := byID[entry.ID]
			if !exists {
				removed[entry.ID] = true
				continue
			}
			if entry.SessionID != next.SessionID || entry.SessionPath != next.SessionPath {
				next.restoreBlocked = true
				next.restoreBlockReason = "identity_repair_write_failed"
			}
			entry = next
		}
		kept = append(kept, entry)
	}
	original.Tabs = kept
	if len(removed) > 0 {
		repairReconciledTabSelection(&original, removed)
	}
	return original
}

// reconcileSavedTabs filters only presentation entries whose durable identity
// is conclusively gone. It runs before restored tabs are published, so a stale
// entry cannot acquire a controller or lease. It never archives formal sessions.
func (a *App) reconcileSavedTabs(ctx context.Context, file desktopTabsFile) (desktopTabsFile, bool) {
	file.Tabs = append([]desktopTabEntry(nil), file.Tabs...)
	if len(file.Tabs) == 0 {
		file.Tabs = []desktopTabEntry{}
		return file, false
	}

	fast := a.loadSavedTabReconcileEvidence(ctx)
	decisions := make([]savedTabReconcileDecision, len(file.Tabs))
	needsMigration := make([]bool, len(file.Tabs))
	anyNeedsMigration := false
	repairedIdentity := false
	for index := range file.Tabs {
		// Retire submission ownership while retaining any formal session identity.
		// Never restore an old draft operation or consult its historical database.
		if strings.HasPrefix(file.Tabs[index].CreateOperationID, "draft-op-") {
			if sessionID, found, conflict := savedTabPendingSessionIdentity(file.Tabs[index], fast); found && !conflict {
				file.Tabs[index].SessionID = sessionID
			}
			file.Tabs[index].CreateOperationID = ""
			repairedIdentity = true
		}
		if candidate := savedTabRouteCandidateForPath(file.Tabs[index].SessionPath); candidate.kind != "" {
			decisions[index].identityKind = candidate.kind
			needsMigration[index] = true
			anyNeedsMigration = true
			continue
		}
		if sessionID, found, conflict := savedTabPendingSessionIdentity(file.Tabs[index], fast); found && !conflict {
			file.Tabs[index].SessionID = sessionID
			repairedIdentity = true
		}
		entry := file.Tabs[index]
		decision, final := a.classifySavedTab(entry, fast, false)
		decisions[index] = decision
		needsMigration[index] = !final
		anyNeedsMigration = anyNeedsMigration || !final
	}

	if anyNeedsMigration {
		migrationFinished := a.waitForDesktopMigration(ctx)
		afterMigration := a.loadSavedTabReconcileEvidence(ctx)
		for index := range file.Tabs {
			if !needsMigration[index] {
				continue
			}
			identityKind := decisions[index].identityKind
			if !migrationFinished {
				decisions[index] = savedTabReconcileDecision{outcome: preserveError, reason: "migration_interrupted", identityKind: identityKind, waitedForMigration: true}
				continue
			}
			if identityKind != "" {
				normalized, repaired, override := a.normalizeSavedTabRoute(ctx, file.Tabs[index], afterMigration)
				if override != nil {
					override.identityKind = identityKind
					override.waitedForMigration = true
					decisions[index] = *override
					continue
				}
				file.Tabs[index] = normalized
				if repaired {
					repairedIdentity = true
				}
			}
			if sessionID, found, conflict := savedTabPendingSessionIdentity(file.Tabs[index], afterMigration); found && !conflict {
				file.Tabs[index].SessionID = sessionID
				repairedIdentity = true
			}
			if source := a.savedTabHistoricalSource(file.Tabs[index], afterMigration); source != nil {
				file.Tabs[index].historicalSource = source
				decisions[index] = savedTabReconcileDecision{outcome: preserveRecovery, reason: "historical_source_pending", waitedForMigration: true}
				continue
			}
			decision, _ := a.classifySavedTab(file.Tabs[index], afterMigration, true)
			if identityKind != "" {
				decision.identityKind = identityKind
			}
			decision.repairedIdentity = strings.TrimSpace(file.Tabs[index].SessionID) != "" && strings.TrimSpace(file.Tabs[index].SessionPath) == "" && identityKind != ""
			decision.waitedForMigration = true
			decisions[index] = decision
		}
	}

	var removed map[string]bool
	file.Tabs, removed = filterReconciledSavedTabs(file.Tabs, decisions)
	if len(removed) == 0 && !repairedIdentity {
		return file, false
	}
	repairReconciledTabSelection(&file, removed)
	return file, true
}

func filterReconciledSavedTabs(tabs []desktopTabEntry, decisions []savedTabReconcileDecision) ([]desktopTabEntry, map[string]bool) {
	filtered := make([]desktopTabEntry, 0, len(tabs))
	removed := map[string]bool{}
	for index, entry := range tabs {
		decision := decisions[index]
		if decision.outcome == dropStalePresentation {
			removed[entry.ID] = true
		} else {
			if (entry.SessionPath != "" || entry.SessionID != "") && (decision.outcome == preserveError || decision.outcome == preserveRecovery) {
				entry.restoreBlocked = true
				entry.restoreBlockReason = decision.reason
			}
			filtered = append(filtered, entry)
		}
		if decision.outcome == restoreTab && !decision.waitedForMigration && !decision.repairedIdentity {
			continue
		}
		identityKind := decision.identityKind
		if identityKind == "" {
			identityKind = savedTabIdentityKind(entry)
		}
		slog.Info("desktop_saved_tab_reconciled",
			"outcome", decision.outcome,
			"reason", decision.reason,
			"identity_kind", identityKind,
			"waited_for_migration", decision.waitedForMigration,
			"had_pending_operation", decision.hadPending,
			"had_recovery_owner", decision.hadRecoveryOwner,
		)
	}
	return filtered, removed
}

func (a *App) waitForDesktopMigration(ctx context.Context) bool {
	if a == nil || a.desktopMigrationDone == nil {
		return false
	}
	if a.beforeSavedTabMigrationWait != nil {
		a.beforeSavedTabMigrationWait()
	}
	select {
	case <-a.desktopMigrationDone:
		return !a.desktopMigrationFailed.Load()
	case <-ctx.Done():
		return false
	}
}

func (a *App) loadSavedTabReconcileEvidence(ctx context.Context) savedTabReconcileEvidence {
	evidence := savedTabReconcileEvidence{}
	evidence.registry, evidence.registryErr = a.workspaceRegistry().Load(ctx)
	return evidence
}

func (a *App) classifySavedTab(entry desktopTabEntry, evidence savedTabReconcileEvidence, afterMigration bool) (savedTabReconcileDecision, bool) {
	if !afterMigration {
		if evidence.registryErr != nil {
			return savedTabReconcileDecision{}, false
		}
		if savedTabHasMatchingPendingCreate(entry, evidence) {
			return savedTabReconcileDecision{outcome: restoreTab, reason: "pending_create", hadPending: true}, true
		}
	}

	if sessionID := strings.TrimSpace(entry.SessionID); sessionID != "" {
		return a.classifyCanonicalSavedTab(entry, sessionID, evidence, afterMigration)
	}
	if strings.TrimSpace(entry.SessionPath) != "" {
		if !afterMigration {
			return savedTabReconcileDecision{}, false
		}
		if _, ok, err := legacySessionPathForFileAccess(entry.SessionPath); err != nil || !ok {
			return savedTabReconcileDecision{outcome: preserveError, reason: "invalid_legacy_path", identityKind: "invalid_legacy"}, true
		}
		return a.classifyLegacySavedTab(entry, evidence), true
	}
	if strings.TrimSpace(entry.CreateOperationID) != "" && !afterMigration {
		return savedTabReconcileDecision{}, false
	}
	if strings.TrimSpace(entry.CreateOperationID) != "" {
		return savedTabReconcileDecision{outcome: preserveError, reason: "pending_identity_unresolved", hadPending: true}, true
	}
	if afterMigration && evidence.registryErr != nil {
		return savedTabReconcileDecision{outcome: preserveError, reason: "persistence_state_unavailable"}, true
	}
	return savedTabReconcileDecision{outcome: dropStalePresentation, reason: "identity_absent"}, true
}

func (a *App) classifyCanonicalSavedTab(entry desktopTabEntry, sessionID string, evidence savedTabReconcileEvidence, afterMigration bool) (savedTabReconcileDecision, bool) {
	if evidence.registryErr != nil {
		if afterMigration {
			return savedTabReconcileDecision{outcome: preserveError, reason: "persistence_state_unavailable"}, true
		}
		return savedTabReconcileDecision{}, false
	}
	if savedTabHasMatchingPendingCreate(entry, evidence) {
		return savedTabReconcileDecision{outcome: restoreTab, reason: "pending_create", hadPending: true}, true
	}

	status, registered := evidence.registry.SessionStates[sessionID]
	if registered && (status.Lifecycle == workspacestate.Archived || status.Lifecycle == workspacestate.Deleted) {
		return savedTabReconcileDecision{outcome: dropStalePresentation, reason: "inactive_lifecycle"}, true
	}
	info, statErr := a.desktopSessionService("").Query().Stat(a.bootContext(), session.SessionRef{HostID: localDesktopHostID, SessionID: sessionID})
	if !afterMigration {
		if registered && status.Lifecycle == workspacestate.Active && statErr == nil {
			workspace, consistent := savedTabCanonicalWorkspace(evidence.registry, info, sessionID)
			if consistent && savedTabMatchesWorkspace(entry, workspace) {
				return savedTabReconcileDecision{outcome: restoreTab, reason: "canonical_active"}, true
			}
		}
		return savedTabReconcileDecision{}, false
	}

	recoveryOwner := savedTabHasRecoveryOwner(entry, evidence)
	if registered && status.Lifecycle == workspacestate.Active {
		if errors.Is(statErr, session.ErrSessionNotFound) || errors.Is(statErr, os.ErrNotExist) {
			return savedTabReconcileDecision{outcome: preserveRecovery, reason: "registered_session_missing", hadRecoveryOwner: true}, true
		}
		if statErr != nil || info.MetadataStatus == session.MetadataFailed {
			return savedTabReconcileDecision{outcome: preserveError, reason: "canonical_session_unreadable", hadRecoveryOwner: recoveryOwner}, true
		}
		workspace, consistent := savedTabCanonicalWorkspace(evidence.registry, info, sessionID)
		if !consistent {
			if recoveryOwner {
				return savedTabReconcileDecision{outcome: preserveRecovery, reason: "recovery_owner_present", hadRecoveryOwner: true}, true
			}
			return savedTabReconcileDecision{outcome: preserveError, reason: "canonical_workspace_conflict", hadRecoveryOwner: recoveryOwner}, true
		}
		if !savedTabMatchesWorkspace(entry, workspace) {
			return savedTabReconcileDecision{outcome: restoreTab, reason: "repair_workspace"}, true
		}
		return savedTabReconcileDecision{outcome: restoreTab, reason: "canonical_active"}, true
	}
	if statErr == nil {
		return savedTabReconcileDecision{outcome: preserveRecovery, reason: "canonical_session_unregistered", hadRecoveryOwner: true}, true
	}
	if !errors.Is(statErr, session.ErrSessionNotFound) && !errors.Is(statErr, os.ErrNotExist) {
		return savedTabReconcileDecision{outcome: preserveError, reason: "canonical_session_unreadable", hadRecoveryOwner: recoveryOwner}, true
	}
	if recoveryOwner {
		return savedTabReconcileDecision{outcome: preserveRecovery, reason: "recovery_owner_present", hadRecoveryOwner: true}, true
	}
	if present, err := a.savedTabCanonicalArtifactsPresent(entry, sessionID); err != nil {
		return savedTabReconcileDecision{outcome: preserveError, reason: "canonical_artifacts_unreadable"}, true
	} else if present {
		return savedTabReconcileDecision{outcome: preserveRecovery, reason: "canonical_artifacts_present", hadRecoveryOwner: true}, true
	}
	return savedTabReconcileDecision{outcome: dropStalePresentation, reason: "canonical_identity_absent"}, true
}

func (a *App) classifyLegacySavedTab(entry desktopTabEntry, evidence savedTabReconcileEvidence) savedTabReconcileDecision {
	if evidence.registryErr != nil {
		return savedTabReconcileDecision{outcome: preserveError, reason: "persistence_state_unavailable"}
	}
	path := strings.TrimSpace(entry.SessionPath)
	mapping, mapped, mappingErr := selectedSavedTabSourceMapping(path, evidence.registry)
	if mappingErr != nil {
		return savedTabReconcileDecision{outcome: preserveError, reason: "legacy_mapping_unreadable"}
	}
	if mapped {
		switch evidence.registry.SessionStates[mapping.SessionID].Lifecycle {
		case workspacestate.Active:
			return savedTabReconcileDecision{outcome: restoreTab, reason: "legacy_source_mapped", hadRecoveryOwner: true}
		case workspacestate.Archived, workspacestate.Deleted:
			fingerprint, err := desktopSourceFingerprint(path)
			if errors.Is(err, os.ErrNotExist) {
				if neverWritten, nwErr := legacySessionNeverWritten(path); nwErr == nil && neverWritten {
					return savedTabReconcileDecision{outcome: dropStalePresentation, reason: "mapped_session_inactive"}
				}
				if _, artifactErr := legacyCleanupSourceFingerprint(path); artifactErr == nil {
					return savedTabReconcileDecision{outcome: preserveRecovery, reason: "legacy_artifacts_present", hadRecoveryOwner: true}
				} else if !errors.Is(artifactErr, os.ErrNotExist) {
					return savedTabReconcileDecision{outcome: preserveError, reason: "legacy_artifacts_unreadable"}
				}
				return savedTabReconcileDecision{outcome: dropStalePresentation, reason: "mapped_session_inactive"}
			}
			if err != nil {
				return savedTabReconcileDecision{outcome: preserveError, reason: "legacy_artifacts_unreadable"}
			}
			if mapping.Fingerprint != "" && fingerprint == mapping.Fingerprint {
				return savedTabReconcileDecision{outcome: dropStalePresentation, reason: "mapped_session_inactive"}
			}
			return savedTabReconcileDecision{outcome: preserveRecovery, reason: "legacy_source_changed", hadRecoveryOwner: true}
		default:
			return savedTabReconcileDecision{outcome: preserveRecovery, reason: "source_mapping_incomplete", hadRecoveryOwner: true}
		}
	}
	if savedTabHasRecoveryOwner(entry, evidence) {
		return savedTabReconcileDecision{outcome: preserveRecovery, reason: "recovery_owner_present", hadRecoveryOwner: true}
	}
	if neverWritten, err := legacySessionNeverWritten(path); err != nil {
		return savedTabReconcileDecision{outcome: preserveError, reason: "legacy_artifacts_unreadable"}
	} else if neverWritten {
		return savedTabReconcileDecision{outcome: dropStalePresentation, reason: "legacy_session_never_written"}
	}
	if _, err := legacyCleanupSourceFingerprint(path); err == nil {
		return savedTabReconcileDecision{outcome: preserveRecovery, reason: "legacy_artifacts_present", hadRecoveryOwner: true}
	} else if !errors.Is(err, os.ErrNotExist) {
		return savedTabReconcileDecision{outcome: preserveError, reason: "legacy_artifacts_unreadable"}
	}
	return savedTabReconcileDecision{outcome: dropStalePresentation, reason: "legacy_identity_absent"}
}

func (a *App) savedTabCanonicalArtifactsPresent(entry desktopTabEntry, sessionID string) (bool, error) {
	canonicalPath := filepath.Join(a.desktopSessions.root, sessionID)
	if _, err := os.Lstat(canonicalPath); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	legacyPath := strings.TrimSpace(entry.SessionPath)
	if legacyPath == "" {
		return false, nil
	}
	if _, err := legacyCleanupSourceFingerprint(legacyPath); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return false, nil
}

func selectedSavedTabSourceMapping(path string, state workspacestate.State) (workspacestate.SourceMapping, bool, error) {
	for _, mapping := range state.SourceMappings {
		if mapping.HeadID == "" && sessionRuntimeKey(mapping.Path) == sessionRuntimeKey(path) {
			return mapping, true, nil
		}
	}
	hasHeadMapping := false
	for _, mapping := range state.SourceMappings {
		if mapping.HeadID != "" && sessionRuntimeKey(mapping.Path) == sessionRuntimeKey(path) {
			hasHeadMapping = true
			break
		}
	}
	if !hasHeadMapping {
		return workspacestate.SourceMapping{}, false, nil
	}
	heads, err := agent.ListSessionHeads(path)
	if err != nil {
		return workspacestate.SourceMapping{}, false, err
	}
	for _, head := range heads {
		if !head.Selected || head.Retired {
			continue
		}
		for _, mapping := range state.SourceMappings {
			if mapping.HeadID == head.ID && sessionRuntimeKey(mapping.Path) == sessionRuntimeKey(path) {
				return mapping, true, nil
			}
		}
		return workspacestate.SourceMapping{}, false, nil
	}
	return workspacestate.SourceMapping{}, false, nil
}

func savedTabHasMatchingPendingCreate(entry desktopTabEntry, evidence savedTabReconcileEvidence) bool {
	operationID := strings.TrimSpace(entry.CreateOperationID)
	sessionID := strings.TrimSpace(entry.SessionID)
	if operationID == "" || sessionID == "" {
		return false
	}
	if pending, ok := evidence.registry.PendingCreates[sessionID]; ok && pending.OperationID == operationID {
		return true
	}
	return false
}

func savedTabPendingSessionIdentity(entry desktopTabEntry, evidence savedTabReconcileEvidence) (string, bool, bool) {
	if strings.TrimSpace(entry.SessionID) != "" {
		return "", false, false
	}
	operationID := strings.TrimSpace(entry.CreateOperationID)
	if operationID == "" || evidence.registryErr != nil {
		return "", false, false
	}
	resolved := ""
	accept := func(sessionID string) bool {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return true
		}
		if resolved != "" && resolved != sessionID {
			return false
		}
		resolved = sessionID
		return true
	}
	for sessionID, pending := range evidence.registry.PendingCreates {
		if pending.OperationID == operationID && !accept(sessionID) {
			return "", false, true
		}
	}
	for _, operation := range evidence.registry.PendingOperations {
		if strings.TrimSpace(operation.ID) != operationID {
			continue
		}
		for _, sessionID := range operation.SessionIDs {
			if !accept(sessionID) {
				return "", false, true
			}
		}
		if operation.Mapping != nil && !accept(operation.Mapping.SessionID) {
			return "", false, true
		}
	}
	return resolved, resolved != "", false
}

func savedTabHasRecoveryOwner(entry desktopTabEntry, evidence savedTabReconcileEvidence) bool {
	sessionID := strings.TrimSpace(entry.SessionID)
	pathKey := sessionRuntimeKey(entry.SessionPath)
	if pending, ok := evidence.registry.PendingCreates[sessionID]; sessionID != "" && ok && pending.SessionID == sessionID && !strings.HasPrefix(pending.OperationID, "draft-op-") {
		return true
	}
	for _, operation := range evidence.registry.PendingOperations {
		if sessionID != "" && slices.Contains(operation.SessionIDs, sessionID) {
			return true
		}
		if operation.Mapping != nil && ((sessionID != "" && operation.Mapping.SessionID == sessionID) || (pathKey != "" && sessionRuntimeKey(operation.Mapping.Path) == pathKey)) {
			return true
		}
	}
	for _, mapping := range evidence.registry.SourceMappings {
		if (sessionID != "" && mapping.SessionID == sessionID) || (pathKey != "" && sessionRuntimeKey(mapping.Path) == pathKey) {
			return true
		}
	}
	for _, recovery := range evidence.registry.RecoveryEntries {
		if (sessionID != "" && recovery.SessionID == sessionID) || (pathKey != "" && sessionRuntimeKey(recovery.Path) == pathKey) {
			return true
		}
	}
	return false
}

func savedTabCanonicalWorkspace(state workspacestate.State, info session.SessionInfo, sessionID string) (workspacestate.Workspace, bool) {
	var owner workspacestate.Workspace
	for _, workspace := range state.Workspaces {
		if !slices.Contains(workspace.SessionIDs, sessionID) {
			continue
		}
		if owner.ID != "" {
			return workspacestate.Workspace{}, false
		}
		owner = workspace
	}
	if owner.ID == "" || info.Origin == "" || strings.TrimSpace(info.CWD) == "" || !sameDesktopPath(info.CWD, owner.Root) {
		return workspacestate.Workspace{}, false
	}
	return owner, true
}

func savedTabMatchesWorkspace(entry desktopTabEntry, workspace workspacestate.Workspace) bool {
	return restoredWorkspaceID(entry) == workspace.ID && sameDesktopPath(desktopWorkspaceRoot(entry.Scope, entry.WorkspaceRoot), workspace.Root)
}

func repairReconciledTabSelection(file *desktopTabsFile, removed map[string]bool) {
	local := make(map[string]bool, len(file.Tabs))
	remote := make(map[string]bool, len(file.RemoteTabs))
	for _, entry := range file.Tabs {
		local[entry.ID] = true
	}
	for _, entry := range file.RemoteTabs {
		remote[entry.ID] = true
	}
	order := make([]string, 0, len(file.TabOrder))
	for _, id := range file.TabOrder {
		if !removed[id] && (local[id] || remote[id]) {
			order = append(order, id)
		}
	}
	file.TabOrder = order
	if local[file.ActiveTab] || remote[file.ActiveTab] {
		return
	}
	file.ActiveTab = ""
	if len(order) > 0 {
		file.ActiveTab = order[0]
	} else if len(file.Tabs) > 0 {
		file.ActiveTab = file.Tabs[0].ID
	} else if len(file.RemoteTabs) > 0 {
		file.ActiveTab = file.RemoteTabs[0].ID
	}
}

func savedTabIdentityKind(entry desktopTabEntry) string {
	if strings.TrimSpace(entry.SessionID) != "" {
		return "canonical"
	}
	if strings.TrimSpace(entry.SessionPath) != "" {
		return "legacy"
	}
	return "none"
}

// legacySessionNeverWritten reports a legacy session with no transcript, event
// log, turn log or subagent: a chat opened and never sent. Its remaining
// sidecars describe a conversation that does not exist, so nothing is owed.
func legacySessionNeverWritten(sessionPath string) (bool, error) {
	for _, path := range []string{
		sessionPath,
		store.SessionEventLog(sessionPath),
		store.SessionEventLogDamaged(sessionPath),
		store.SessionEventLogRotating(sessionPath),
		store.SessionTurnEventLog(sessionPath),
		store.SessionTurnEventLogDamaged(sessionPath),
	} {
		if _, err := os.Lstat(path); err == nil {
			return false, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	subagents, err := agent.ListSubagentsByParent(filepath.Dir(sessionPath), agent.BranchID(sessionPath))
	if err != nil {
		return false, err
	}
	return len(subagents) == 0, nil
}
