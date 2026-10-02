package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"reasonix/desktop/internal/legacycleanup"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/store"
	"reasonix/internal/transcript"
)

type legacyCleanupSourceTarget struct {
	state   workspacestate.State
	mapping workspacestate.SourceMapping
	ref     session.SessionRef
	frozen  legacycleanup.Candidate
}

func (a *App) processLegacyCleanupSource(item legacycleanup.Candidate) {
	target, classification, reason, ok := a.resolveLegacyCleanupSourceTarget(item)
	if !ok {
		a.setLegacyCleanupSourceOutcome(item.ID, "", classification, reason)
		return
	}
	if a.reconcileLegacyCleanupArchivedOperation(item, target.mapping.SessionID) {
		return
	}
	target, classification, reason, ok = a.freezeLegacyCleanupSourceTarget(item, target)
	if !ok {
		a.setLegacyCleanupSourceOutcome(item.ID, target.mapping.SessionID, classification, reason)
		return
	}
	decision := a.classifyLegacyCleanupSession(a.bootContext(), target.ref, target.frozen)
	if decision.classification != "empty" {
		a.setLegacyCleanupSourceOutcome(item.ID, target.mapping.SessionID, decision.classification, decision.reason)
		return
	}
	if a.legacyCleanupWorker.beforeArchive != nil {
		a.legacyCleanupWorker.beforeArchive()
	}
	a.archiveLegacyCleanupSource(item, target)
}

func (a *App) resolveLegacyCleanupSourceTarget(item legacycleanup.Candidate) (legacyCleanupSourceTarget, string, string, bool) {
	classification, reason, _ := classifyLegacyCleanupSource(item.SourcePath, item.SourceHeadID, item.SourceFingerprint)
	if classification != "empty" {
		return legacyCleanupSourceTarget{}, classification, reason, false
	}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return legacyCleanupSourceTarget{}, "unknown", "workspace_unavailable", false
	}
	mapping, ok, resolveErr := state.ResolveSource(desktopSourceKey(item.SourcePath, item.SourceHeadID))
	if resolveErr != nil {
		return legacyCleanupSourceTarget{}, "protected", "migration_identity_changed", false
	}
	if !ok {
		return legacyCleanupSourceTarget{}, "unknown", "legacy_session_requires_migration", false
	}
	if !sameDesktopPath(mapping.Path, item.SourcePath) || mapping.HeadID != item.SourceHeadID || mapping.WorkspaceID != item.WorkspaceID {
		return legacyCleanupSourceTarget{}, "protected", "migration_identity_changed", false
	}
	return legacyCleanupSourceTarget{state: state, mapping: mapping}, "", "", true
}

func (a *App) freezeLegacyCleanupSourceTarget(item legacycleanup.Candidate, target legacyCleanupSourceTarget) (legacyCleanupSourceTarget, string, string, bool) {
	status, ok := target.state.SessionStates[target.mapping.SessionID]
	if !ok || status.Lifecycle != workspacestate.Active {
		return target, "protected", "lifecycle_changed", false
	}
	target.ref = session.SessionRef{HostID: localDesktopHostID, SessionID: target.mapping.SessionID}
	info, err := a.desktopSessionService("").Query().Stat(a.bootContext(), target.ref)
	if err != nil || info.MetadataStatus == session.MetadataFailed {
		return target, "unknown", "session_metadata_unavailable", false
	}
	target.frozen = item
	if item.SessionID == "" {
		target.frozen.SessionID = target.mapping.SessionID
		target.frozen.WorkspaceID = target.mapping.WorkspaceID
		target.frozen.TitleSequence = info.TitleSequence
		target.frozen.EventSequence = info.EventSequence
		target.frozen.LifecycleGeneration = status.Generation
	} else if item.SessionID != target.mapping.SessionID || item.WorkspaceID != target.mapping.WorkspaceID {
		return target, "protected", "migration_binding_changed", false
	}
	return target, "", "", true
}

func (a *App) archiveLegacyCleanupSource(item legacycleanup.Candidate, target legacyCleanupSourceTarget) {
	releaseRuntime, ok := a.tryLockRuntimeMutation("legacy empty source cleanup")
	if !ok {
		a.setLegacyCleanupSourceOutcome(item.ID, target.mapping.SessionID, "busy", "runtime_mutation")
		return
	}
	defer releaseRuntime()
	sourceGuard, err := acquireSessionRemovalGuard(item.SourcePath)
	if err != nil {
		classification, reason := "unknown", "legacy_source_lock_failed"
		if errors.Is(err, agent.ErrSessionLeaseHeld) {
			classification, reason = "busy", "legacy_source_busy"
		}
		a.setLegacyCleanupSourceOutcome(item.ID, target.mapping.SessionID, classification, reason)
		return
	}
	defer sourceGuard.Release()
	verify := func(ctx context.Context, latest workspacestate.State) error {
		current, exists, resolveErr := latest.ResolveSource(desktopSourceKey(item.SourcePath, item.SourceHeadID))
		if resolveErr != nil || !exists || current.SessionID != target.mapping.SessionID || current.WorkspaceID != target.mapping.WorkspaceID ||
			!sameDesktopPath(current.Path, item.SourcePath) || current.HeadID != item.SourceHeadID {
			return fmt.Errorf("%w: migration mapping changed", errLegacyCleanupStateChanged)
		}
		if sourceClass, _, _ := classifyLegacyCleanupSource(item.SourcePath, item.SourceHeadID, item.SourceFingerprint); sourceClass != "empty" {
			return fmt.Errorf("%w: legacy source is %s", errLegacyCleanupStateChanged, sourceClass)
		}
		fresh := a.classifyLegacyCleanupSession(ctx, target.ref, target.frozen)
		if fresh.classification != "empty" {
			return fmt.Errorf("%w: canonical session is %s", errLegacyCleanupStateChanged, fresh.classification)
		}
		return nil
	}
	err = a.archiveSessionRefsWithOperationConditional([]session.SessionRef{target.ref}, item.OperationID, verify)
	if err != nil {
		classification, reason := legacyCleanupArchiveError(err)
		a.setLegacyCleanupSourceOutcome(item.ID, target.mapping.SessionID, classification, reason)
		return
	}
	a.updateLegacyCleanupItem(item.ID, func(next *legacycleanup.Candidate) {
		next.SessionID = target.mapping.SessionID
		next.Phase, next.Classification, next.Reason, next.ArchivedAt = "archived", "empty", "", time.Now().UTC().UnixMilli()
	})
}

func legacyCleanupArchiveError(err error) (string, string) {
	if errors.Is(err, errTopicHasActiveWork) || errors.Is(err, errTopicArchiveBusy) || errors.Is(err, agent.ErrSessionLeaseHeld) {
		return "busy", "runtime_active"
	}
	if errors.Is(err, errLegacyCleanupStateChanged) || errors.Is(err, workspacestate.ErrMutationConflict) {
		return "protected", "state_changed"
	}
	return "unknown", "archive_failed"
}

func (a *App) setLegacyCleanupSourceOutcome(id, sessionID, classification, reason string) {
	a.updateLegacyCleanupItem(id, func(next *legacycleanup.Candidate) {
		if sessionID != "" {
			next.SessionID = sessionID
		}
		next.Phase, next.Classification, next.Reason = classification, classification, reason
	})
}
func (a *App) bindLegacyCleanupMigration(ctx context.Context, path, headID, sessionID, workspaceID string) error {
	if a == nil || a.legacyCleanup == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: sessionID}
	info, err := a.desktopSessionService("").Query().RefreshMetadata(ctx, ref)
	if err != nil {
		return err
	}
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	status, ok := state.SessionStates[sessionID]
	if !ok || status.Lifecycle != workspacestate.Active {
		return workspacestate.ErrMutationConflict
	}
	id := "legacy:" + desktopSourceKey(path, headID)
	_, err = a.legacyCleanup.Update(ctx, func(cleanup *legacycleanup.State) error {
		item, exists := cleanup.Items[id]
		if !exists {
			return nil
		}
		if item.Kind != "legacy" || !sameDesktopPath(item.SourcePath, path) || item.SourceHeadID != headID || item.WorkspaceID != workspaceID {
			return errLegacyCleanupStateChanged
		}
		if item.SessionID != "" && item.SessionID != sessionID {
			return errLegacyCleanupStateChanged
		}
		item.SessionID = sessionID
		item.TitleSequence = info.TitleSequence
		item.EventSequence = info.EventSequence
		item.LifecycleGeneration = status.Generation
		cleanup.Items[id] = item
		return nil
	})
	return err
}

func (a *App) classifyLegacyCleanupSession(ctx context.Context, ref session.SessionRef, frozen legacycleanup.Candidate) legacyCleanupDecision {
	state, decision, ok := a.classifyLegacyCleanupRegistry(ctx, ref, frozen)
	if !ok {
		return decision
	}
	if a.legacyCleanupSessionIsOpen(ref.SessionID) {
		return legacyCleanupDecision{"busy", "session_open", session.SessionInfo{}, session.Snapshot{}}
	}
	service := a.desktopSessionService("")
	if _, live := service.Runtime(ref); live {
		return legacyCleanupDecision{"busy", "runtime_open", session.SessionInfo{}, session.Snapshot{}}
	}
	return classifyLegacyCleanupCanonicalStorage(ctx, service, state, ref, frozen)
}

func (a *App) classifyLegacyCleanupRegistry(ctx context.Context, ref session.SessionRef, frozen legacycleanup.Candidate) (workspacestate.State, legacyCleanupDecision, bool) {
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return state, legacyCleanupDecision{"unknown", "workspace_unavailable", session.SessionInfo{}, session.Snapshot{}}, false
	}
	status, ok := state.SessionStates[ref.SessionID]
	if !ok || status.Lifecycle != workspacestate.Active || status.Generation != frozen.LifecycleGeneration {
		return state, legacyCleanupDecision{"protected", "lifecycle_changed", session.SessionInfo{}, session.Snapshot{}}, false
	}
	workspace, ok := state.Workspaces[frozen.WorkspaceID]
	if !ok || !slices.Contains(workspace.SessionIDs, ref.SessionID) {
		return state, legacyCleanupDecision{"protected", "workspace_changed", session.SessionInfo{}, session.Snapshot{}}, false
	}
	if state.Presentation[ref.SessionID].Pinned {
		return state, legacyCleanupDecision{"has_content", "session_pinned", session.SessionInfo{}, session.Snapshot{}}, false
	}
	for _, pending := range state.PendingCreates {
		if pending.SessionID == ref.SessionID || pending.ParentSessionID == ref.SessionID {
			return state, legacyCleanupDecision{"protected", "create_or_derivation", session.SessionInfo{}, session.Snapshot{}}, false
		}
	}
	if decision, owned := classifyLegacyCleanupPersistentOwner(state, ref, frozen); owned {
		return state, decision, false
	}
	if decision, valid := classifyLegacyCleanupSourceMappings(state, ref, frozen); !valid {
		return state, decision, false
	}
	ops, err := a.draftStore().PendingOperations(ctx)
	if err != nil {
		return state, legacyCleanupDecision{"unknown", "draft_state_unavailable", session.SessionInfo{}, session.Snapshot{}}, false
	}
	for _, op := range ops {
		if op.SessionID == ref.SessionID {
			return state, legacyCleanupDecision{"protected", "draft_operation", session.SessionInfo{}, session.Snapshot{}}, false
		}
	}
	return state, legacyCleanupDecision{}, true
}

func classifyLegacyCleanupSourceMappings(state workspacestate.State, ref session.SessionRef, frozen legacycleanup.Candidate) (legacyCleanupDecision, bool) {
	for _, source := range frozen.Sources {
		mapping, exists, resolveErr := state.ResolveSource(desktopSourceKey(source.Path, source.HeadID))
		if resolveErr != nil || !exists || mapping.SessionID != ref.SessionID || mapping.WorkspaceID != frozen.WorkspaceID || mapping.Format != "legacy" ||
			!sameDesktopPath(mapping.Path, source.Path) || mapping.HeadID != source.HeadID {
			return legacyCleanupDecision{"protected", "legacy_mapping_changed", session.SessionInfo{}, session.Snapshot{}}, false
		}
		classification, reason, _ := classifyLegacyCleanupSource(source.Path, source.HeadID, source.Fingerprint)
		if classification != "empty" {
			return legacyCleanupDecision{classification, reason, session.SessionInfo{}, session.Snapshot{}}, false
		}
	}
	return legacyCleanupDecision{}, true
}

func (a *App) legacyCleanupSessionIsOpen(sessionID string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, tabs := range []map[string]*WorkspaceTab{a.tabs, a.detachedSessions} {
		for _, tab := range tabs {
			if tab != nil && tab.SessionID == sessionID {
				return true
			}
		}
	}
	return false
}

func classifyLegacyCleanupCanonicalStorage(ctx context.Context, service *session.Service, state workspacestate.State, ref session.SessionRef, frozen legacycleanup.Candidate) legacyCleanupDecision {
	info, err := service.Query().Stat(ctx, ref)
	if err != nil || info.MetadataStatus == session.MetadataFailed {
		return legacyCleanupDecision{"unknown", "session_metadata_unavailable", info, session.Snapshot{}}
	}
	title := state.Presentation[ref.SessionID].Title
	if info.TitleSequence > 0 || strings.TrimSpace(info.Title) != "" {
		title = info.Title
	}
	if !isDefaultTopicTitle(title) || info.TitleSequence != frozen.TitleSequence || info.EventSequence != frozen.EventSequence {
		return legacyCleanupDecision{"protected", "title_or_content_changed", info, session.Snapshot{}}
	}
	if info.ParentSessionID != "" || info.Origin == session.SessionOriginFork {
		return legacyCleanupDecision{"protected", "derived_session", info, session.Snapshot{}}
	}
	snapshot, err := service.Query().Snapshot(ctx, ref)
	if err != nil || snapshot.PersistenceStatus != session.PersistenceReady {
		return legacyCleanupDecision{"unknown", "session_content_unavailable", info, snapshot}
	}
	if info.ResultSequence > 0 || canonicalProjectionHasUserContent(snapshot.Projection) {
		return legacyCleanupDecision{"has_content", "session_content", info, snapshot}
	}
	if classification, reason := canonicalSessionDurableEvidence(ctx, info); classification != "empty" {
		return legacyCleanupDecision{classification, reason, info, snapshot}
	}
	if classification, reason := classifyLegacyCleanupArtifacts(info.Path); classification != "empty" {
		return legacyCleanupDecision{classification, reason, info, snapshot}
	}
	if classification, reason := classifyCanonicalOwnedDirectories(info.Path); classification != "empty" {
		return legacyCleanupDecision{classification, reason, info, snapshot}
	}
	return legacyCleanupDecision{"empty", "", info, snapshot}
}

func classifyCanonicalOwnedDirectories(sessionPath string) (string, string) {
	for _, owned := range []struct {
		name   string
		reason string
	}{
		{name: "attachments", reason: "session_attachment"},
		{name: "assets", reason: "session_asset"},
	} {
		nonempty, err := directoryHasDurableEntries(filepath.Join(sessionPath, owned.name), nil)
		if err != nil {
			return "unknown", owned.reason + "_unavailable"
		}
		if nonempty {
			return "has_content", owned.reason
		}
	}
	return "empty", ""
}
func canonicalProjectionHasUserContent(projection session.Projection) bool {
	for _, message := range projection.Messages {
		if message.Role == provider.RoleUser || message.Role == provider.RoleAssistant || message.Role == provider.RoleTool {
			return true
		}
	}
	if body := strings.TrimSpace(string(projection.GoalState)); body != "" && body != "{}" && body != "null" {
		return true
	}
	return len(projection.Turns) > 0 || projection.TurnID != "" || len(projection.Todos) > 0 || projection.TodoWritten ||
		len(projection.Interactions) > 0 || len(projection.StartedTools) > 0 || len(projection.ActiveSteps) > 0 || projection.Recovery != nil
}

type legacyCleanupEvidence struct {
	classification string
	reason         string
}

func (e *legacyCleanupEvidence) mark(classification, reason string) {
	if e.classification == "has_content" || classification == "empty" {
		return
	}
	if classification == "has_content" || e.classification == "" || e.classification == "empty" {
		e.classification, e.reason = classification, reason
	}
}

func canonicalSessionDurableEvidence(ctx context.Context, info session.SessionInfo) (string, string) {
	evidence := legacyCleanupEvidence{classification: "empty"}
	err := session.VisitCommits(ctx, info.Path, func(commit session.Commit) error {
		for _, event := range commit.Events {
			classification, reason := classifyCanonicalSessionEvent(commit, event)
			evidence.mark(classification, reason)
		}
		return nil
	})
	if err != nil {
		return "unknown", "session_event_log_unavailable"
	}
	return evidence.classification, evidence.reason
}

func classifyCanonicalSessionEvent(commit session.Commit, event session.Event) (string, string) {
	switch event.Kind {
	case "session/title", "diagnostic":
		return "empty", ""
	case "session/config":
		return classifyCanonicalConfigEvent(commit)
	case "message/complete", "message/upsert":
		return classifyCanonicalMessageEvent(event.Payload)
	case "model/context-replace", "history/replace":
		return classifyCanonicalContextEvent(commit, event.Payload)
	case "legacy/import":
		return classifyCanonicalLegacyImportEvent(event.Payload)
	case "submission/accepted", "message/retract", "assistant/attempt", "tool/call", "tool/start", "tool/result",
		"turn/start", "turn/end", "step/start", "step/end", "todo/write", "interaction/created", "interaction/resolved",
		"plan/state", "goal/state", "compaction", "runtime/recovery":
		return "has_content", "execution_event"
	default:
		return "unknown", "unsupported_session_event"
	}
}

func classifyCanonicalConfigEvent(commit session.Commit) (string, string) {
	if commit.OperationID == "session-create" || strings.HasPrefix(commit.OperationID, "legacy-import:") ||
		strings.HasPrefix(commit.OperationID, "legacy-import-config:") || strings.HasPrefix(commit.OperationID, "prototype-import-config:") {
		return "empty", ""
	}
	if strings.HasPrefix(commit.OperationID, "session-model:") {
		return "has_content", "explicit_session_config"
	}
	return "unknown", "unclassified_session_config"
}

func classifyCanonicalMessageEvent(payload json.RawMessage) (string, string) {
	var body struct {
		Message *provider.Message `json:"message"`
	}
	if err := json.Unmarshal(payload, &body); err != nil || body.Message == nil {
		return "unknown", "message_event_unreadable"
	}
	if isLegacyCleanupContentRole(body.Message.Role) {
		return "has_content", "message_event"
	}
	if body.Message.Role != provider.RoleSystem {
		return "unknown", "unknown_message_role"
	}
	return "empty", ""
}

func classifyCanonicalContextEvent(commit session.Commit, raw json.RawMessage) (string, string) {
	var payload struct {
		Messages []provider.Message `json:"messages"`
		Reason   string             `json:"reason"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "unknown", "context_event_unreadable"
	}
	initialization := strings.HasPrefix(commit.OperationID, "legacy-import:") || payload.Reason == "system-prompt-refresh"
	if !onlySystemMessages(payload.Messages) || !initialization {
		return "has_content", "context_event"
	}
	return "empty", ""
}

func classifyCanonicalLegacyImportEvent(raw json.RawMessage) (string, string) {
	var payload struct {
		Messages []provider.Message `json:"messages"`
		Goal     json.RawMessage    `json:"goal"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Messages == nil {
		return "unknown", "legacy_import_unreadable"
	}
	for _, message := range payload.Messages {
		if isLegacyCleanupContentRole(message.Role) {
			return "has_content", "legacy_import_message"
		}
		if message.Role != provider.RoleSystem {
			return "unknown", "legacy_import_message_role"
		}
	}
	goal := bytes.TrimSpace(payload.Goal)
	if len(goal) > 0 && !bytes.Equal(goal, []byte("null")) && !bytes.Equal(goal, []byte("{}")) {
		return "has_content", "legacy_import_goal"
	}
	return "empty", ""
}

func onlySystemMessages(messages []provider.Message) bool {
	for _, message := range messages {
		if message.Role != provider.RoleSystem {
			return false
		}
	}
	return true
}

func isLegacyCleanupContentRole(role provider.Role) bool {
	return role == provider.RoleUser || role == provider.RoleAssistant || role == provider.RoleTool
}
func classifyLegacyCleanupSource(path, headID, frozenFingerprint string) (string, string, string) {
	currentFingerprint, err := legacyCleanupSourceFingerprint(path)
	if err != nil {
		return "unknown", "legacy_source_unavailable", ""
	}
	if frozenFingerprint == "" {
		return "unknown", "legacy_source_was_unreadable", currentFingerprint
	}
	if currentFingerprint != frozenFingerprint {
		return "protected", "legacy_source_changed", currentFingerprint
	}
	if agent.IsCleanupPending(path) {
		return "protected", "legacy_cleanup_pending", currentFingerprint
	}
	var legacySession *agent.Session
	if strings.TrimSpace(headID) == "" {
		legacySession, err = agent.LoadSession(path)
	} else {
		legacySession, err = agent.LoadSessionHeadReadOnly(path, headID)
	}
	if err != nil {
		return "unknown", "legacy_transcript_unavailable", currentFingerprint
	}
	if classification, reason := classifyLegacyEventLog(path); classification != "empty" {
		return classification, reason, currentFingerprint
	}
	for _, message := range legacySession.Snapshot() {
		if message.Role == provider.RoleUser || message.Role == provider.RoleAssistant || message.Role == provider.RoleTool {
			return "has_content", "legacy_message", currentFingerprint
		}
		if message.Role != provider.RoleSystem {
			return "unknown", "legacy_message_role", currentFingerprint
		}
	}
	if meta, exists, err := agent.LoadBranchMeta(path); err != nil {
		return "unknown", "legacy_metadata_unavailable", currentFingerprint
	} else if exists {
		if meta.ParentID != "" || meta.ParentConversationID != "" || meta.ParentVersionID != "" || meta.Recovered ||
			meta.EffectiveVersionKind() != agent.VersionNormal || meta.InFlightTurn != nil {
			return "protected", "legacy_derivation_or_recovery", currentFingerprint
		}
		if strings.TrimSpace(meta.Goal) != "" {
			return "has_content", "legacy_goal", currentFingerprint
		}
		if meta.Model != "" || meta.ModelIdentity != "" || meta.TokenMode != "" || meta.AgentPreset != "" ||
			meta.QualityFloor != "" || meta.Mode != "" || meta.ToolApprovalMode != "" {
			return "unknown", "legacy_explicit_configuration", currentFingerprint
		}
	}
	if classification, reason := classifyLegacyCleanupArtifacts(path); classification != "empty" {
		return classification, reason, currentFingerprint
	}
	return "empty", "", currentFingerprint
}

func classifyLegacyEventLog(path string) (string, string) {
	file, err := os.Open(store.SessionEventLog(path))
	if os.IsNotExist(err) {
		return "empty", ""
	}
	if err != nil {
		return "unknown", "event_log_unavailable"
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		return "unknown", "event_log_unavailable"
	}
	if info.Size() == 0 {
		return "empty", ""
	}
	var header struct {
		SchemaVersion int    `json:"schema_version"`
		Type          string `json:"type"`
	}
	if err := json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(&header); err != nil || header.SchemaVersion < 1 || header.SchemaVersion > 2 || strings.TrimSpace(header.Type) == "" {
		return "unknown", "event_log_unreadable"
	}
	// agent.LoadSession above already replayed and validated supported native
	// records. The log itself is not additional content beyond that projection.
	return "empty", ""
}

func classifyLegacyCleanupArtifacts(path string) (string, string) {
	if state, err := loadPinnedContextState(path); err != nil {
		return "unknown", "pinned_context_unavailable"
	} else if len(state.Files) > 0 {
		return "has_content", "pinned_context"
	}
	if classification, reason := classifyLegacyJSONSidecar(store.SessionGoalState(path), "goal"); classification != "empty" {
		return classification, reason
	}
	for _, target := range []struct {
		path   string
		reason string
	}{
		{store.SessionRecoveryState(path), "recovery_state"},
		{store.SessionTurnEventLog(path), "turn_ledger"},
		{store.SessionTurnEventLogDamaged(path), "damaged_turn_ledger"},
		{store.SessionEventLogDamaged(path), "damaged_event_log"},
		{store.SessionConflictLog(path), "conflict_log"},
		{sessionTelemetryPath(path), "session_telemetry"},
	} {
		info, err := os.Stat(target.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || info.IsDir() {
			return "unknown", target.reason + "_unavailable"
		}
		if info.Size() > 0 {
			return "has_content", target.reason
		}
	}
	if info, err := os.Stat(store.SessionEventLogRotating(path)); err == nil {
		if info.IsDir() || info.Size() > 0 {
			return "unknown", "rotating_event_log_requires_recovery"
		}
	} else if !os.IsNotExist(err) {
		return "unknown", "rotating_event_log_unavailable"
	}
	if classification, reason := classifyTranscriptCheckpoint(path); classification != "empty" {
		return classification, reason
	}
	if classification, reason := classifyContextCheckpoint(path); classification != "empty" {
		return classification, reason
	}
	for _, target := range []struct {
		path   string
		reason string
	}{
		{store.SessionCheckpointDir(path), "checkpoint"},
		{store.SessionJobsDir(path), "background_job"},
	} {
		nonempty, err := directoryHasDurableEntries(target.path, nil)
		if err != nil {
			return "unknown", target.reason + "_unavailable"
		}
		if nonempty {
			return "has_content", target.reason
		}
	}
	if classification, reason := classifyLegacyInbox(path); classification != "empty" {
		return classification, reason
	}
	if subagents, err := agent.ListSubagentsByParent(filepath.Dir(path), agent.BranchID(path)); err != nil {
		return "unknown", "subagent_state_unavailable"
	} else if len(subagents) > 0 {
		return "has_content", "subagent_state"
	}
	return "empty", ""
}

func classifyTranscriptCheckpoint(sessionPath string) (string, string) {
	path := store.SessionTranscriptProjection(sessionPath)
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "empty", ""
	}
	if err != nil {
		return "unknown", "transcript_projection_unavailable"
	}
	var raw map[string]json.RawMessage
	var checkpoint transcript.Checkpoint
	if json.Unmarshal(body, &raw) != nil || json.Unmarshal(body, &checkpoint) != nil || checkpoint.Version != transcript.ProtocolVersion {
		return "unknown", "transcript_projection_unreadable"
	}
	allowed := map[string]bool{
		"version": true, "identity": true, "coveredThroughSeq": true, "transcriptDigest": true,
		"providerCount": true, "records": true, "runtime": true, "activeAttempts": true, "completion": true,
	}
	for field := range raw {
		if !allowed[field] {
			return "unknown", "transcript_projection_requires_verification"
		}
	}
	runtime := checkpoint.Runtime
	if len(checkpoint.Records) > 0 || len(checkpoint.ActiveAttempts) > 0 || checkpoint.Completion != nil ||
		runtime.FinalMessageID != "" || runtime.DurationMs != 0 || runtime.SamplingCount != 0 || runtime.ToolCount != 0 ||
		runtime.TurnID != "" || runtime.SubmissionID != "" || runtime.Status != "" || runtime.Phase != "" || runtime.StartedAt != 0 ||
		len(runtime.PendingEvents) > 0 || runtime.CompletionSummary != nil || runtime.TurnUsage != nil {
		return "has_content", "transcript_projection"
	}
	return "empty", ""
}

func classifyContextCheckpoint(sessionPath string) (string, string) {
	path := store.SessionContext(sessionPath)
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "empty", ""
	}
	if err != nil {
		return "unknown", "session_context_unavailable"
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return "unknown", "session_context_unreadable"
	}
	allowed := map[string]bool{
		"schema_version": true, "transcript_version": true, "projection": true, "prompt_cache_key": true,
		"last_cache_state": true, "last_trigger": true, "last_mode": true, "last_source_tokens": true,
		"last_result_tokens": true, "last_compaction_cost": true, "generation": true, "last_receipt": true,
		"blocked_input_hash": true, "blocked_reason": true, "native_context_editing_accepted": true,
		"context_editing_fallback_local": true, "updated_at": true,
	}
	for field := range raw {
		if !allowed[field] {
			return "unknown", "session_context_requires_verification"
		}
	}
	state, ok, err := agent.LoadCompactionState(sessionPath)
	if err != nil || !ok {
		return "unknown", "session_context_unreadable"
	}
	if compactionStateHasContent(state) {
		return "has_content", "session_context"
	}
	return "empty", ""
}

func compactionStateHasContent(state agent.CompactionState) bool {
	return contextProjectionHasContent(state.Projection) || state.TranscriptVersion != 0 ||
		state.PromptCacheKey != "" || state.LastCacheState != "" || state.LastTrigger != "" || state.LastMode != "" ||
		state.LastSourceTokens != 0 || state.LastResultTokens != 0 || state.LastCompactionCost != 0 || state.Generation != 0 ||
		state.LastReceipt != nil || state.BlockedInputHash != "" || state.BlockedReason != "" ||
		state.NativeContextEditingAccepted || state.ContextEditingFallbackLocal
}

func contextProjectionHasContent(projection agent.ContextProjection) bool {
	return projection.TranscriptVersion != 0 || projection.ProjectionVersion != 0 || projection.CoveredCount != 0 ||
		projection.CoveredPrefixHash != "" || projection.PinnedContextHash != "" || projection.SummaryHash != "" ||
		projection.SourceTokens != 0 || projection.ProjectionTokens != 0 || projection.ViewInputHash != "" ||
		projection.ViewOutputHash != "" || len(projection.Messages) > 0
}

func classifyLegacyJSONSidecar(path, field string) (string, string) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "empty", ""
	}
	if err != nil {
		return "unknown", field + "_state_unavailable"
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return "unknown", field + "_state_unreadable"
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return "unknown", field + "_state_unreadable"
	}
	if raw, ok := object[field]; ok {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "unknown", field + "_state_unreadable"
		}
		if strings.TrimSpace(text) != "" {
			return "has_content", field + "_state"
		}
	}
	return "empty", ""
}

func directoryHasDurableEntries(path string, ignored map[string]bool) (bool, error) {
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if ignored != nil && ignored[entry.Name()] {
			continue
		}
		return true, nil
	}
	return false, nil
}

func classifyLegacyInbox(path string) (string, string) {
	dir := store.SessionInboxDir(path)
	manifestPath := filepath.Join(dir, "manifest.json")
	body, err := os.ReadFile(manifestPath)
	if os.IsNotExist(err) {
		nonempty, readErr := directoryHasDurableEntries(dir, map[string]bool{"transaction.lock": true})
		if readErr != nil {
			return "unknown", "inbox_unavailable"
		}
		if nonempty {
			return "unknown", "inbox_manifest_missing"
		}
		return "empty", ""
	}
	if err != nil {
		return "unknown", "inbox_unavailable"
	}
	var manifest struct {
		SchemaVersion     int                        `json:"schemaVersion"`
		Paused            bool                       `json:"paused"`
		Recovered         bool                       `json:"recovered"`
		RecoveredCount    int                        `json:"recoveredCount"`
		Items             []json.RawMessage          `json:"items"`
		Idempotency       map[string]string          `json:"idempotency"`
		IdempotencyHashes map[string]string          `json:"idempotencyHashes"`
		Receipts          map[string]json.RawMessage `json:"receipts"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil || manifest.SchemaVersion < 1 || manifest.SchemaVersion > 2 {
		return "unknown", "inbox_unreadable"
	}
	if len(manifest.Items) > 0 || manifest.Paused || manifest.Recovered || manifest.RecoveredCount > 0 ||
		len(manifest.Idempotency) > 0 || len(manifest.IdempotencyHashes) > 0 || len(manifest.Receipts) > 0 {
		return "has_content", "inbox_items"
	}
	nonempty, err := directoryHasDurableEntries(dir, map[string]bool{"transaction.lock": true, "manifest.json": true})
	if err != nil {
		return "unknown", "inbox_unavailable"
	}
	if nonempty {
		return "unknown", "inbox_orphan_artifacts"
	}
	return "empty", ""
}
