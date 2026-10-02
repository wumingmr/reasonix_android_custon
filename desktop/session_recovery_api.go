package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/session"
)

type RecoveryEntryView struct {
	WorkspaceChoices []RecoveryWorkspaceChoice `json:"workspaceChoices"`
	ID               string                    `json:"id"`
	Title            string                    `json:"title"`
	Format           string                    `json:"format"`
	Reason           string                    `json:"reason"`
	Status           string                    `json:"status"`
	CanPreview       bool                      `json:"canPreview"`
	CanRestore       bool                      `json:"canRestore"`
}

type RecoveryWorkspaceChoice struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (a *App) recoveryWorkspaceChoices(ctx context.Context, state workspacestate.State, entry workspacestate.RecoveryEntry) []RecoveryWorkspaceChoice {
	allowed := map[string]bool{}
	if entry.SessionID != "" {
		if info, err := a.desktopSessionService("").Query().Stat(ctx, session.SessionRef{HostID: localDesktopHostID, SessionID: entry.SessionID}); err == nil && info.CWD != "" {
			for id, w := range state.Workspaces {
				if sameDesktopPath(w.Root, info.CWD) || (id == workspacestate.GlobalWorkspaceID && isGlobalWorkspacePath(w, info.CWD)) {
					allowed[id] = true
				}
			}
		}
	} else {
		allowed[desktopWorkspaceOwnerID(state, entry.Scope, entry.WorkspaceRoot)] = true
		if entry.WorkspaceID != "" {
			allowed[entry.WorkspaceID] = true
		}
	}
	choices := []RecoveryWorkspaceChoice{}
	for id := range allowed {
		if w, ok := state.Workspaces[id]; ok {
			choices = append(choices, RecoveryWorkspaceChoice{ID: id, Title: w.Title})
		}
	}
	sort.Slice(choices, func(i, j int) bool { return choices[i].ID < choices[j].ID })
	return choices
}

type RecoveryEntryPage struct {
	Items      []RecoveryEntryView `json:"items"`
	NextCursor string              `json:"nextCursor,omitempty"`
	Generation uint64              `json:"generation"`
}
type SessionUpgradeStatus struct {
	Sources           int `json:"sources"`
	Sessions          int `json:"sessions"`
	Operations        int `json:"operations"`
	Discovered        int `json:"discovered"`
	Migrated          int `json:"migrated"`
	Pending           int `json:"pending"`
	Failed            int `json:"failed"`
	Conflicts         int `json:"conflicts"`
	PendingOperations int `json:"pendingOperations"`
}

func (a *App) GetSessionUpgradeStatus() (SessionUpgradeStatus, error) {
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return SessionUpgradeStatus{}, err
	}
	sessions := map[string]bool{}
	for _, mapping := range state.SourceMappings {
		sessions[mapping.SessionID] = true
	}
	result := SessionUpgradeStatus{Sources: len(state.SourceMappings), Sessions: len(sessions), Operations: len(state.PendingOperations), Migrated: len(sessions), Discovered: len(state.SourceMappings) + len(state.RecoveryEntries)}
	for _, entry := range state.RecoveryEntries {
		if entry.Status == "restored" {
			continue
		}
		result.Pending++
		if entry.Status == "failed" {
			result.Failed++
		}
		if strings.Contains(entry.Reason, "conflict") {
			result.Conflicts++
		}
	}
	for _, op := range state.PendingOperations {
		if op.Phase != "committed" {
			result.PendingOperations++
		}
	}
	return result, nil
}

func (a *App) ListRecoveryEntries(query, cursor string, limit int) (RecoveryEntryPage, error) {
	out := RecoveryEntryPage{Items: []RecoveryEntryView{}}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return out, err
	}
	out.Generation = state.Generation
	start, err := decodeWorkspaceSessionCursor(cursor, state.Generation)
	if err != nil {
		return out, err
	}
	ids := []string{}
	for id, entry := range state.RecoveryEntries {
		if entry.Status == "restored" {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(filepath.Base(entry.Path)+" "+entry.Reason+" "+id), strings.ToLower(query)) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if start > len(ids) {
		start = len(ids)
	}
	end := min(start+limit, len(ids))
	for _, id := range ids[start:end] {
		entry := state.RecoveryEntries[id]
		title := filepath.Base(entry.Path)
		if entry.SessionID != "" {
			title = entry.SessionID
		}
		available := entry.SessionID != "" || (entry.Path != "" && (entry.Format == "legacy" || entry.Format == "legacy-trash" || entry.Format == "canonical"))
		choices := a.recoveryWorkspaceChoices(a.bootContext(), state, entry)
		canRestore := !strings.Contains(entry.Reason, "conflict") || (entry.Reason == "workspace_conflict" && len(choices) > 0)
		out.Items = append(out.Items, RecoveryEntryView{WorkspaceChoices: choices, ID: id, Title: title, Format: entry.Format, Reason: entry.Reason, Status: entry.Status, CanPreview: available, CanRestore: available && canRestore})
	}
	if end < len(ids) {
		out.NextCursor = fmt.Sprintf("%d:%d", state.Generation, end)
	}
	return out, nil
}

func (a *App) checkedRecoveryEntry(ctx context.Context, id string) (workspacestate.RecoveryEntry, error) {
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return workspacestate.RecoveryEntry{}, err
	}
	entry, ok := state.RecoveryEntries[id]
	if !ok {
		return entry, errors.New("recovery entry is unavailable")
	}
	if entry.Path != "" {
		switch entry.Format {
		case "legacy-trash":
			if _, err := a.trashedSessionDir(entry.Path); err != nil {
				return entry, err
			}
		case "legacy":
			if _, _, err := a.sessionDirForPath(entry.Path); err != nil {
				return entry, err
			}
		case "canonical":
			allowed := sameDesktopPath(filepath.Dir(entry.Path), config.SessionStoreDir()) || sameDesktopPath(filepath.Dir(entry.Path), config.ProjectSessionStoreDir(globalWorkspaceRoot()))
			for _, workspace := range state.Workspaces {
				allowed = allowed || sameDesktopPath(filepath.Dir(entry.Path), config.ProjectSessionStoreDir(workspace.Root))
			}
			if !allowed {
				return entry, errors.New("canonical recovery source is outside known storage roots")
			}
		default:
			return entry, errors.New("this historical format requires migration repair")
		}
		if _, err := desktopSourceFingerprint(entry.Path); err != nil {
			return entry, err
		}
	}
	return entry, nil
}

func (a *App) PreviewRecoveryEntry(id string) (HistoryPage, error) {
	entry, err := a.checkedRecoveryEntry(a.bootContext(), id)
	if err != nil {
		return HistoryPage{Messages: []HistoryMessage{}}, err
	}
	if entry.SessionID != "" {
		return a.ReadSessionHistory(session.SessionRef{HostID: localDesktopHostID, SessionID: entry.SessionID}, "", 32)
	}
	if entry.Format == "canonical" {
		if preview, err := isDesktopStoredPreview(entry.Path); err != nil {
			return HistoryPage{Messages: []HistoryMessage{}}, err
		} else if preview {
			return previewStoredRecovery(a.bootContext(), entry.Path)
		}
		old, err := session.NewService("recovery-preview", session.NewFilesystemPersistence(filepath.Dir(entry.Path)))
		if err != nil {
			return HistoryPage{Messages: []HistoryMessage{}}, err
		}
		defer func() { _ = old.Shutdown(context.Background()) }()
		messages, err := old.Query().History(a.bootContext(), session.SessionRef{HostID: "recovery-preview", SessionID: filepath.Base(entry.Path)})
		if err != nil {
			return HistoryPage{Messages: []HistoryMessage{}}, err
		}
		return historyPageFromProviderMessages(messages, func(value string) string { return value }, nil, nil, 0, 32), nil
	}
	if entry.HeadID != "" {
		loaded, err := agent.LoadSessionHeadReadOnly(entry.Path, entry.HeadID)
		if err != nil {
			return HistoryPage{Messages: []HistoryMessage{}}, err
		}
		return historyPageFromProviderMessages(loaded.Messages, func(value string) string { return value }, nil, nil, 0, 32), nil
	}
	return previewSessionPage(filepath.Dir(entry.Path), entry.Path, 0, 32)
}

func (a *App) RestoreRecoveryEntry(id, operationID string) (SessionRestoreResult, error) {
	return a.restoreRecoveryEntryInWorkspace(id, operationID, "")
}

func (a *App) restoreRecoveryEntryInWorkspace(id, operationID, workspaceID string) (SessionRestoreResult, error) {
	ctx, finish, err := a.beginHistoricalRecovery()
	if err != nil {
		return SessionRestoreResult{}, err
	}
	defer finish()
	if operationID == "" {
		operationID = "restore-recovery-" + id
	}
	if operationID != "" {
		state, err := a.workspaceRegistry().Load(ctx)
		if err != nil {
			return SessionRestoreResult{}, err
		}
		if previous, ok := state.PendingOperations[operationID]; ok && previous.Phase == "committed" {
			if previous.RecoveryEntryID != id || len(previous.SessionIDs) != 1 {
				return SessionRestoreResult{}, workspacestate.ErrMutationConflict
			}
			return SessionRestoreResult{Session: session.SessionRef{HostID: localDesktopHostID, SessionID: previous.SessionIDs[0]}, WorkspaceID: previous.WorkspaceID, Generation: previous.ResultGeneration}, nil
		}
	}
	entry, err := a.checkedRecoveryEntry(ctx, id)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	entry, err = a.selectRecoveryWorkspace(ctx, entry, workspaceID)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	if err := validateRecoveryConflict(entry, workspaceID); err != nil {
		return SessionRestoreResult{}, err
	}
	if entry.SessionID != "" {
		return a.restoreCanonicalSession(ctx, session.SessionRef{HostID: localDesktopHostID, SessionID: entry.SessionID}, operationID, id)
	}
	release, err := acquireHistoricalSource(ctx, desktopSourceKey(entry.Path, entry.HeadID), historicalSource{path: entry.Path, format: entry.Format})
	if err != nil {
		return SessionRestoreResult{}, err
	}
	defer release()
	fingerprint, err := desktopSourceFingerprint(entry.Path)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	if entry.Fingerprint != fingerprint {
		rebased, err := a.workspaceRegistry().RebaseRecovery(ctx, id, fingerprint)
		if err != nil {
			return SessionRestoreResult{}, err
		}
		if rebased.SessionID != "" {
			return a.restoreCanonicalSession(ctx, session.SessionRef{HostID: localDesktopHostID, SessionID: rebased.SessionID}, operationID, id)
		}
		entry.Fingerprint = rebased.Fingerprint
	}
	workspaceID, err = a.ensureDesktopWorkspace(ctx, entry.Scope, entry.WorkspaceRoot)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	if previous, ok := state.PendingOperations[operationID]; ok && previous.Phase == "committed" {
		if previous.RecoveryEntryID != id || len(previous.SessionIDs) != 1 {
			return SessionRestoreResult{}, workspacestate.ErrMutationConflict
		}
		return SessionRestoreResult{Session: session.SessionRef{HostID: localDesktopHostID, SessionID: previous.SessionIDs[0]}, WorkspaceID: previous.WorkspaceID, Generation: previous.ResultGeneration}, nil
	}
	op := workspacestate.Operation{ID: operationID, Kind: "restore", RecoveryEntryID: id, WorkspaceID: workspaceID, Lifecycle: workspacestate.Active, ExpectedGeneration: state.Generation}
	if err := a.workspaceRegistry().BeginOperation(ctx, op); err != nil {
		return SessionRestoreResult{}, err
	}
	source := desktopMigrationSource{scope: entry.Scope, workspaceRoot: entry.WorkspaceRoot, operationID: operationID, headID: entry.HeadID}
	if entry.Reason == "source_changed_after_adoption" {
		source.versionFingerprint = fingerprint
	}
	err = a.convertHistoricalSource(ctx, historicalSource{path: entry.Path, format: entry.Format}, source, workspaceID)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	state, err = a.workspaceRegistry().Load(ctx)
	if err != nil {
		return SessionRestoreResult{}, err
	}
	op = state.PendingOperations[operationID]
	if op.Phase != "committed" || len(op.SessionIDs) != 1 {
		return SessionRestoreResult{}, errors.New("historical restore is pending")
	}
	a.emitProjectTreeChanged()
	return SessionRestoreResult{Session: session.SessionRef{HostID: localDesktopHostID, SessionID: op.SessionIDs[0]}, WorkspaceID: workspaceID, Generation: op.ResultGeneration}, nil
}

func (a *App) discoverHistoricalTrash(ctx context.Context) error {
	var joined error
	for _, dir := range a.knownSessionDirs() {
		if err := ctx.Err(); err != nil {
			return err
		}
		paths, err := listTrashedSessionFiles(dir)
		if err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		for _, path := range paths {
			scope, root := "global", ""
			if meta, ok, err := agent.LoadBranchMeta(path); err == nil && ok && meta.WorkspaceRoot != "" && !a.isGlobalWorkspacePath(ctx, meta.WorkspaceRoot) {
				scope, root = "project", meta.WorkspaceRoot
			}
			// Old "deleted" entries were recoverable trash, not permanent
			// deletion. Preserve that affordance in the single archived list.
			if explicitlyDeletedLegacyEntry(path) {
				fingerprint, err := desktopSourceFingerprint(path)
				if err != nil {
					joined = errors.Join(joined, err)
					continue
				}
				source := desktopMigrationSource{scope: scope, workspaceRoot: root, deferArchive: true}
				if err := a.migrateLegacySession(ctx, path, source, ""); err != nil {
					joined = errors.Join(joined, err)
					continue
				}
				state, err := a.workspaceRegistry().Load(ctx)
				if err != nil {
					joined = errors.Join(joined, err)
					continue
				}
				opID := "archive-import-" + desktopSourceKey(path, "") + "-" + fingerprint
				if op, ok := state.PendingOperations[opID]; ok && op.Phase == "content_ready" {
					joined = errors.Join(joined, a.workspaceRegistry().CommitHistoricalArchive(ctx, opID, trashedSessionDeletedAt(path)))
				}
				continue
			}
			if err := a.sourceRecovery(ctx, path, "legacy-trash", "historical_state_unknown", scope, root); err != nil {
				joined = errors.Join(joined, err)
			}
			joined = errors.Join(joined, a.discoverLegacyHeads(ctx, path, "legacy-trash", scope, root))
		}
	}
	return joined
}

func explicitlyDeletedLegacyEntry(path string) bool {
	body, err := os.ReadFile(filepath.Join(filepath.Dir(path), sessionTrashMetaFile))
	if err != nil {
		return false
	}
	var meta trashedSessionMeta
	return json.Unmarshal(body, &meta) == nil && meta.Kind == "deleted"
}

func (a *App) reconcileUnregisteredSessions(ctx context.Context) error {
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for id, status := range state.SessionStates {
		if status.Lifecycle == workspacestate.Deleted {
			known[id] = true
		}
	}
	for _, workspace := range state.Workspaces {
		for _, id := range workspace.SessionIDs {
			known[id] = true
		}
	}
	for id := range state.PendingCreates {
		known[id] = true
	}
	for _, op := range state.PendingOperations {
		if op.Phase != "committed" {
			for _, id := range op.SessionIDs {
				known[id] = true
			}
		}
	}
	infos, err := listAllCanonicalSessionInfo(ctx, a.desktopSessionService("").Query())
	if err != nil {
		return err
	}
	var joined error
	entries, readErr := os.ReadDir(a.desktopSessions.root)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || known[entry.Name()] {
			continue
		}
		if _, listed := infos[entry.Name()]; listed {
			continue
		}
		path := filepath.Join(a.desktopSessions.root, entry.Name())
		if _, err := os.Lstat(filepath.Join(path, "manifest.json")); os.IsNotExist(err) {
			continue
		}
		joined = errors.Join(joined, a.workspaceRegistry().ReconcileDiscoveredSession(ctx, workspacestate.RecoveryEntry{
			ID: "canonical-" + entry.Name(), SourceKey: "canonical:" + entry.Name(), SessionID: entry.Name(), Format: "canonical", Reason: "unreadable_content", Status: "failed",
		}, nil))
	}
	for id, info := range infos {
		if known[id] {
			continue
		}
		ref := session.SessionRef{HostID: localDesktopHostID, SessionID: id}
		reason := ""
		if info.Origin == "" || strings.TrimSpace(info.CWD) == "" {
			reason = "workspace_conflict"
		}
		if _, err := a.desktopSessionService("").Query().Snapshot(ctx, ref); err != nil {
			reason = "unreadable_content"
		}
		if status, ok := state.SessionStates[id]; ok && status.Lifecycle != workspacestate.Active {
			reason = "historical_state_unknown"
		}
		if reason != "" {
			err := a.workspaceRegistry().ReconcileDiscoveredSession(ctx, workspacestate.RecoveryEntry{ID: "canonical-" + id, SourceKey: "canonical:" + id, SessionID: id, Format: "canonical", Reason: reason, Status: "pending"}, nil)
			joined = errors.Join(joined, err)
			continue
		}
		scope, root := "project", info.CWD
		if isGlobalWorkspacePath(state.Workspaces[workspacestate.GlobalWorkspaceID], root) {
			scope, root = "global", ""
		}
		title := workspaceName(root)
		if scope == "global" {
			title = globalProjectTitle()
		}
		err := a.workspaceRegistry().ReconcileDiscoveredSession(ctx,
			workspacestate.RecoveryEntry{ID: "canonical-" + id, SourceKey: "canonical:" + id, SessionID: id, Format: "canonical", Status: "pending"},
			&workspacestate.Workspace{ID: desktopWorkspaceID(scope, root), Root: desktopWorkspaceRoot(scope, root), Title: title, Visible: true})
		joined = errors.Join(joined, err)
	}
	return joined
}

func (a *App) recoverDesktopSessionOperations(ctx context.Context) error {
	return a.recoverDesktopOperations(ctx, true)
}

func (a *App) recoverDesktopOperations(ctx context.Context, includeHistorical bool) error {
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	replay := func(op workspacestate.Operation) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !includeHistorical && (op.Kind == "import" || op.Kind == "restore" || op.Kind == "archive-import") {
			return nil
		}
		release, ok := a.tryLockRuntimeMutation("replay session lifecycle")
		if !ok {
			return errTopicArchiveBusy
		}
		defer release()
		return a.replayDesktopSessionOperation(ctx, state, op)
	}
	joined := a.reconcileTopicRemovals(state)
	for _, op := range state.PendingOperations {
		if op.Kind != "purge" || op.Phase == "committed" {
			continue
		}
		joined = errors.Join(joined, replay(op))
	}
	for _, op := range state.PendingOperations {
		if op.Phase == "committed" || op.Kind == "archive-import" || op.Kind == "command" || op.Kind == "purge" {
			continue
		}
		joined = errors.Join(joined, replay(op))
	}
	for _, op := range state.PendingOperations {
		if op.Kind != "command" || op.Phase == "committed" {
			continue
		}
		var req SessionLifecycleRequest
		if err := json.Unmarshal(op.Request, &req); err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		if !includeHistorical && req.Action == "restore" {
			continue
		}
		_, err := a.ApplySessionLifecycle(req)
		joined = errors.Join(joined, err)
	}
	return joined
}

func (a *App) replayDesktopSessionOperation(ctx context.Context, state workspacestate.State, op workspacestate.Operation) error {
	if op.Kind == "purge" {
		if len(op.SessionIDs) != 1 {
			return workspacestate.ErrMutationConflict
		}
		if err := a.resumeCanonicalPurge(ctx, session.SessionRef{HostID: localDesktopHostID, SessionID: op.SessionIDs[0]}, op); err != nil {
			return fmt.Errorf("replay purge session=%s phase=%s expected_generation=%d: %w", op.SessionIDs[0], op.Phase, op.ExpectedGeneration, err)
		}
		return nil
	}
	releaseSources, err := freezeArchiveDependencies(ctx, state, op)
	if err != nil {
		return err
	}
	defer releaseSources()
	if err := validateDesktopOperationSources(state, op); err != nil {
		return err
	}
	if op.Phase == "prepared" && op.Mapping != nil {
		return a.replayPreparedImport(ctx, state, op)
	}
	if len(op.SessionIDs) == 0 {
		return workspacestate.ErrMutationConflict
	}
	guards := []func(){}
	defer func() {
		for _, release := range slices.Backward(guards) {
			release()
		}
	}()
	removed := []removedSessionRuntime{}
	if op.Lifecycle == workspacestate.Archived || op.Lifecycle == workspacestate.Deleted {
		var err error
		removed, err = a.idleArchiveRuntimes(op.SessionIDs, nil)
		if err != nil {
			return err
		}
	}
	service := a.desktopSessionService("")
	for _, id := range op.SessionIDs {
		ref := session.SessionRef{HostID: localDesktopHostID, SessionID: id}
		if runtime, live := service.Runtime(ref); live {
			phase := runtime.StateSnapshot().Phase
			if phase != session.RuntimeIdle && phase != session.RuntimeRecoveryRequired {
				return errTopicHasActiveWork
			}
		} else {
			guard, err := session.NewFilesystemPersistence(a.desktopSessions.root).AcquireMaintenance(id)
			if err != nil {
				return err
			}
			guards = append(guards, guard)
		}
		if _, err := service.Query().Snapshot(ctx, ref); err != nil {
			return err
		}
		if op.WorkspaceID != "" {
			if err := a.validateDesktopWorkspaceMembership(ctx, op.WorkspaceID, ref); err != nil {
				return err
			}
		}
	}
	if err := a.validateRecoveredHistoricalArchive(ctx, state, op); err != nil {
		return err
	}
	if op.Phase == "prepared" {
		if err := a.workspaceRegistry().PrepareOperationContent(ctx, op.ID, op.SessionIDs, op.Mapping, op.Presentation); err != nil {
			return err
		}
	}
	if err := a.workspaceRegistry().CommitOperation(ctx, op.ID); err != nil {
		return err
	}
	if len(removed) > 0 {
		a.finishArchivedRuntimeBindings(removed)
	}
	return nil
}

func (a *App) restoreLegacyRecoveryPath(path string) error {
	dir, err := a.trashedSessionDir(path)
	if err != nil {
		return err
	}
	_, key, _, err := validateTrashedSessionPath(dir, path)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, key)
	if a.sessionDestroying(dir, target) || agent.IsCleanupPending(target) {
		return fmt.Errorf("session cleanup is still in progress: %s", key)
	}
	if a.sessionOpen(dir, target) {
		return fmt.Errorf("session is open: %s", key)
	}
	scope, root := "global", ""
	if meta, ok, err := agent.LoadBranchMeta(path); err == nil && ok && meta.WorkspaceRoot != "" && !a.isGlobalWorkspacePath(a.bootContext(), meta.WorkspaceRoot) {
		scope, root = "project", meta.WorkspaceRoot
	}
	if err := a.sourceRecovery(a.bootContext(), path, "legacy-trash", "historical_state_unknown", scope, root); err != nil {
		return err
	}
	fingerprint, err := desktopSourceFingerprint(path)
	if err != nil {
		return err
	}
	_, err = a.RestoreRecoveryEntry(desktopRecoveryID(desktopSourceKey(path, ""), fingerprint), "")
	return err
}

func (a *App) selectRecoveryWorkspace(ctx context.Context, entry workspacestate.RecoveryEntry, workspaceID string) (workspacestate.RecoveryEntry, error) {
	if workspaceID != "" {
		state, err := a.workspaceRegistry().Load(ctx)
		if err != nil {
			return entry, err
		}
		valid := false
		for _, choice := range a.recoveryWorkspaceChoices(ctx, state, entry) {
			valid = valid || choice.ID == workspaceID
		}
		if !valid {
			return entry, errors.New("recovery workspace is not an allowed destination")
		}
		w := state.Workspaces[workspaceID]
		entry.Scope, entry.WorkspaceRoot = "project", w.Root
		if workspaceID == workspacestate.GlobalWorkspaceID {
			entry.Scope, entry.WorkspaceRoot = "global", ""
		}
	}
	return entry, nil
}

func (a *App) replayPreparedImport(ctx context.Context, state workspacestate.State, op workspacestate.Operation) error {
	mapping := op.Mapping
	workspace, ok := state.Workspaces[op.WorkspaceID]
	if !ok {
		return workspacestate.ErrWorkspaceNotFound
	}
	scope := "project"
	if workspace.ID == workspacestate.GlobalWorkspaceID {
		scope = "global"
	}
	source := desktopMigrationSource{scope: scope, workspaceRoot: workspace.Root, operationID: op.ID, headID: mapping.HeadID}
	if strings.HasPrefix(op.ID, "repair-") {
		source.registeredSourceKey = mapping.SourceKey
		old, ref, finish, err := openHistoricalReconciliationSource(ctx, historicalSource{path: mapping.Path, format: mapping.Format, head: mapping.HeadID})
		if err != nil {
			return err
		}
		defer finish()
		return a.repairMissingHistoricalTarget(ctx, old, ref, source, mapping.Path, mapping.SessionID, mapping.Fingerprint)
	}
	if mapping.SourceKey == desktopSourceKey(mapping.Path, mapping.HeadID)+":review:"+mapping.Fingerprint {
		source.versionFingerprint = mapping.Fingerprint
	}
	if mapping.Format == "legacy" {
		return a.migrateLegacySession(ctx, mapping.Path, source, workspace.ID)
	}
	if mapping.Format == "canonical" {
		source.root = filepath.Dir(mapping.Path)
		old, err := session.NewService("migration-source", session.NewFilesystemPersistence(source.root))
		if err != nil {
			return err
		}
		defer func() { _ = old.Shutdown(context.Background()) }()
		return a.migrateCanonicalSession(ctx, old, source, workspace.ID, filepath.Base(mapping.Path))
	}
	return workspacestate.ErrUnsupportedVersion
}

func validateRecoveryConflict(entry workspacestate.RecoveryEntry, workspaceID string) error {
	if strings.Contains(entry.Reason, "conflict") && !(entry.Reason == "workspace_conflict" && workspaceID != "") {
		return errors.New("historical session sources conflict; originals were preserved")
	}
	return nil
}
