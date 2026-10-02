package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/sessioncatalog"
)

const (
	aiSessionTitleMaxTurns     = 3
	aiSessionTitleMaxTurnRunes = 500
)

type aiSessionTitleOperation struct {
	ID     string
	Cancel context.CancelCauseFunc
}

// AIRenameSession generates a title from the explicitly targeted durable
// conversation. The argument also accepts a canonical session route or legacy
// path, which takes precedence over the compatibility topic lookup.
func (a *App) AIRenameSession(topicID string) (string, error) {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return "", newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	selector := sessionTargetSelector{TopicID: topicID}
	if _, ok := parseSessionRoute(topicID); ok || strings.ContainsAny(topicID, `/\`) {
		selector = sessionTargetSelector{SessionPath: topicID}
	}
	result, err := a.AIRenameSessionTarget(selector)
	return result.Title, err
}

// AIRenameSessionTarget generates and commits a title for one explicit durable
// target without selecting it or constructing a conversation controller.
func (a *App) AIRenameSessionTarget(selector SessionSelector) (SessionMutationResult, error) {
	target, err := a.resolveSessionMutationTarget(selector)
	if err != nil {
		return SessionMutationResult{}, err
	}
	key := target.key()
	operationID := "title-ai-" + strings.TrimPrefix(newTabID(), "tab_")
	if strings.TrimSpace(target.SessionRef.SessionID) == "" && strings.TrimSpace(target.SessionPath) == "" {
		return SessionMutationResult{}, sessionOperationErrorForTarget(
			newSessionOperationError(sessionOperationNoMessages, "This session has no user messages to analyze."),
			key, operationID,
		)
	}
	operationCtx, operationCancel := context.WithCancelCause(a.bootContext())
	a.aiSessionTitleMu.Lock()
	if a.aiSessionTitleInFlight == nil {
		a.aiSessionTitleInFlight = map[string]aiSessionTitleOperation{}
	}
	if _, busy := a.aiSessionTitleInFlight[key]; busy {
		a.aiSessionTitleMu.Unlock()
		operationCancel(context.Canceled)
		return SessionMutationResult{}, sessionOperationErrorForTarget(
			newSessionOperationError(sessionOperationBusy, "AI rename is already running for this session."),
			key, operationID,
		)
	}
	a.aiSessionTitleInFlight[key] = aiSessionTitleOperation{ID: operationID, Cancel: operationCancel}
	a.aiSessionTitleMu.Unlock()
	defer func() {
		operationCancel(context.Canceled)
		a.finishAISessionTitle(key, operationID)
	}()

	var title string
	if strings.TrimSpace(target.SessionRef.SessionID) != "" {
		title, err = a.aiRenameCanonicalSession(operationCtx, target)
	} else {
		title, err = a.aiRenameLegacySession(operationCtx, target)
	}
	if err != nil {
		if cause := context.Cause(operationCtx); cause != nil && !errors.Is(cause, context.Canceled) {
			err = cause
		}
		return SessionMutationResult{}, sessionOperationErrorForTarget(err, key, operationID)
	}
	result := SessionMutationResult{
		TargetKey: key, OperationID: operationID, Committed: true, Title: title,
		LifecycleGeneration: target.LifecycleGeneration,
	}
	if target.SessionRef.SessionID != "" {
		if info, statErr := a.desktopSessionService("").Query().Stat(a.bootContext(), target.SessionRef); statErr == nil {
			result.TitleVersion = titleSequenceVersion(info.TitleSequence)
		} else {
			result.ProjectionPending = true
		}
	} else if _, revision, revisionErr := agent.SessionTitleSnapshot(target.SessionPath); revisionErr == nil {
		result.TitleVersion = revision
	} else {
		result.ProjectionPending = true
	}
	a.emitSessionTargetChange("session_metadata_changed", SessionTargetChangeEvent{
		TargetKey: key, OperationID: operationID, LifecycleGeneration: target.LifecycleGeneration,
		Title: title, WorkspaceID: target.WorkspaceID,
	})
	return result, nil
}

func (a *App) aiRenameLegacySession(ctx context.Context, target SessionTarget) (string, error) {
	sessionDir, validated, err := a.sessionDirForPath(target.SessionPath)
	if err != nil {
		return "", newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	expectedRevision := ""
	modelRef := ""
	if meta, ok, loadErr := agent.LoadBranchMeta(validated); loadErr != nil {
		return "", fmt.Errorf("AI rename session: read current title: %w", loadErr)
	} else if ok {
		modelRef = meta.Model
	}
	_, expectedRevision, err = agent.SessionTitleSnapshot(validated)
	if err != nil {
		return "", fmt.Errorf("AI rename session: read title revision: %w", err)
	}
	users, err := loadTopicTitleUserTurnsFromSession(validated)
	if err != nil {
		return "", fmt.Errorf("AI rename session: read conversation: %w", err)
	}
	if len(users) == 0 {
		return "", newSessionOperationError(sessionOperationNoMessages, "This session has no user messages to analyze.")
	}
	title, err := a.generateTargetSessionTitle(ctx, target, modelRef, sessionTitleTranscript(users))
	if err != nil {
		return "", err
	}
	a.lifecycleCheckpoint("session-title-before-commit")
	a.sessionRemovalMu.Lock()
	defer a.sessionRemovalMu.Unlock()
	a.topicTitleMutationMu.Lock()
	defer a.topicTitleMutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if a.sessionTargetRuntimeRebound(target) {
		return "", newSessionOperationError("target_changed", "The session moved or changed state. Try again.")
	}
	// A legacy source may have been imported and archived while generation
	// was in flight. Original files remain on disk after canonical archive.
	if _, found, err := a.legacyCanonicalRef(ctx, validated); err != nil {
		return "", err
	} else if found {
		return "", newSessionOperationError("target_changed", "The session moved or changed state. Try again.")
	}
	if err := a.renameSessionInDirIfTitleUnchanged(sessionDir, validated, expectedRevision, title); err != nil {
		if errors.Is(err, agent.ErrSessionTitleChanged) {
			return "", newSessionOperationError(sessionOperationTitleConflict, "The session title changed while AI rename was running. Try again.")
		}
		return "", err
	}
	return title, nil
}

// The model round trip is already bounded where it is made: control's session
// title call gives the provider its own budget. This operation therefore runs
// on the caller's cancellation only, like aiRenameLegacySession. A second
// host-level wall clock here would also bound the durable snapshot, the flush,
// the history projection TitleMessages builds and the conditional commit —
// work whose cost scales with the conversation, not with provider health — so
// a long session on a slow host would lose an already generated title to a
// deadline that belongs to the provider.
func (a *App) aiRenameCanonicalSession(ctx context.Context, target SessionTarget) (string, error) {
	service := a.desktopSessionService("")
	ref := target.SessionRef
	snapshot, err := service.Query().Snapshot(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("AI rename session: read current title: %w", err)
	}
	// Publish accepted user turns before querying durable history when the
	// target has a live runtime. Cold sessions are already fully durable.
	if runtime, ok := service.Runtime(ref); ok {
		if _, err := runtime.Session().Flush(ctx); err != nil {
			return "", fmt.Errorf("AI rename session: flush conversation: %w", err)
		}
	}
	messages, err := service.Query().TitleMessages(ctx, ref, aiSessionTitleMaxTurns)
	if err != nil {
		return "", fmt.Errorf("AI rename session: read conversation: %w", err)
	}
	var users []string
	for _, message := range messages {
		if content := topicTitleUserText(message); content != "" {
			users = append(users, content)
		}
	}
	if len(users) == 0 {
		return "", newSessionOperationError(sessionOperationNoMessages, "This session has no user messages to analyze.")
	}
	title, err := a.generateTargetSessionTitle(ctx, target, snapshot.Projection.ModelRef, sessionTitleTranscript(users))
	if err != nil {
		return "", err
	}
	a.lifecycleCheckpoint("session-title-before-commit")
	// Serialize against manual writes; the session title sequence is the
	// authority even if another branch is created during provider generation.
	a.sessionRemovalMu.Lock()
	defer a.sessionRemovalMu.Unlock()
	a.topicTitleMutationMu.Lock()
	defer a.topicTitleMutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if a.sessionTargetRuntimeRebound(target) {
		return "", newSessionOperationError("target_changed", "The session moved or changed state. Try again.")
	}
	if err := a.workspaceRegistry().WithSessionUnchanged(ctx, ref.SessionID, target.WorkspaceID, target.LifecycleGeneration, func() error {
		return service.SetTitleIfSequence(ctx, ref, snapshot.Projection.TitleSequence, title)
	}); err != nil {
		return "", sessionOperationConflict(err)
	}
	a.publishCanonicalSessionTitle(ref, title)
	return title, nil
}

func (a *App) cancelAISessionTitle(targetKey string) {
	targetKey = strings.TrimSpace(targetKey)
	if targetKey == "" {
		return
	}
	a.aiSessionTitleMu.Lock()
	operation := a.aiSessionTitleInFlight[targetKey]
	if operation.ID != "" {
		delete(a.aiSessionTitleInFlight, targetKey)
	}
	a.aiSessionTitleMu.Unlock()
	if operation.Cancel != nil {
		operation.Cancel(newSessionOperationError(sessionOperationTitleConflict, "The session title changed while AI rename was running. Try again."))
	}
}

func (a *App) invalidateAuxiliaryProviderOperations() {
	if a == nil {
		return
	}
	a.auxiliaryProviderGeneration.Add(1)
	a.aiSessionTitleMu.Lock()
	operations := make([]aiSessionTitleOperation, 0, len(a.aiSessionTitleInFlight))
	for key, operation := range a.aiSessionTitleInFlight {
		operations = append(operations, operation)
		delete(a.aiSessionTitleInFlight, key)
	}
	a.aiSessionTitleMu.Unlock()
	for _, operation := range operations {
		if operation.Cancel != nil {
			operation.Cancel(newSessionOperationError(
				"provider_unavailable",
				"The session's model provider changed while AI rename was running. Try again.",
			))
		}
	}
}

func (a *App) finishAISessionTitle(targetKey, operationID string) {
	a.aiSessionTitleMu.Lock()
	defer a.aiSessionTitleMu.Unlock()
	// A manual rename can cancel an operation and a later request may already
	// own the same target. A stale completion must not clear that newer
	// request's deduplication entry.
	if current, ok := a.aiSessionTitleInFlight[targetKey]; ok && current.ID == operationID {
		delete(a.aiSessionTitleInFlight, targetKey)
	}
}

func (a *App) generateTargetSessionTitle(ctx context.Context, target SessionTarget, modelRef, transcript string) (string, error) {
	providerGeneration := a.auxiliaryProviderGeneration.Load()
	if cause := context.Cause(ctx); cause != nil {
		return "", cause
	}
	var (
		title string
		err   error
	)
	if target.Controller != nil {
		title, err = target.Controller.GenerateSessionTitleForModel(ctx, modelRef, transcript)
	} else {
		root := target.WorkspaceRoot
		if target.Scope == "global" || root == "" {
			root = globalWorkspaceRoot()
		}
		cfg, loadErr := config.LoadModelRuntimeSnapshot(root, modelRef)
		if loadErr != nil {
			return "", newSessionOperationError("provider_unavailable", "Unable to load model settings. Check the session's provider configuration.")
		}
		if modelRef == "" {
			modelRef = cfg.DefaultModel
		}
		sessionID := strings.TrimSpace(target.SessionRef.SessionID)
		if sessionID == "" {
			sessionID = strings.TrimSpace(target.TopicID)
		}
		if sessionID == "" {
			sessionID = sessionRuntimeKey(target.SessionPath)
		}
		handle, acquireErr := boot.AcquireAuxiliaryProvider(ctx, boot.AuxiliaryProviderRequest{
			Config: cfg, SessionID: sessionID, WorkspaceRoot: root, ModelRef: modelRef,
		})
		if acquireErr != nil {
			return "", newSessionOperationError("provider_unavailable", "The session's model provider is unavailable. Check its model or extension configuration.")
		}
		defer func() { _ = handle.Close() }()
		title, err = control.GenerateSessionTitleWithResolver(ctx, handle.Resolver, modelRef, transcript)
	}
	if err != nil {
		return "", err
	}
	if cause := context.Cause(ctx); cause != nil {
		return "", cause
	}
	if a.auxiliaryProviderGeneration.Load() != providerGeneration {
		return "", newSessionOperationError(
			"provider_unavailable",
			"The session's model provider changed while AI rename was running. Try again.",
		)
	}
	return title, nil
}

func (a *App) controllerForTopic(topicID string) *control.Controller {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var found *control.Controller
	for _, tab := range a.runtimeTabsLocked() {
		if tab == nil || (strings.TrimSpace(tab.TopicID) != topicID && tab.SessionID != topicID && sessionRoute(tab.SessionID) != topicID) || tab.Ctrl == nil {
			continue
		}
		if ctrl, ok := tab.Ctrl.(*control.Controller); ok {
			if tab.ID == a.activeTabID {
				return ctrl
			}
			if found != nil && found != ctrl {
				return nil
			}
			found = ctrl
		}
	}
	return found
}

func topicTitleUserText(message provider.Message) string {
	if !agent.IsUserAuthoredTurnMessage(message) {
		return ""
	}
	content := control.StripComposePrefixes(agent.UserPreviewText(agent.UserMessageText(message)))
	return strings.TrimSpace(control.StripReferencedContextPrefix(content))
}

func sessionTitleTranscript(users []string) string {
	parts := make([]string, 0, aiSessionTitleMaxTurns)
	for _, user := range users {
		if len(parts) >= aiSessionTitleMaxTurns {
			break
		}
		user = strings.TrimSpace(user)
		if runes := []rune(user); len(runes) > aiSessionTitleMaxTurnRunes {
			user = string(runes[:aiSessionTitleMaxTurnRunes])
		}
		if user != "" {
			parts = append(parts, user)
		}
	}
	return strings.Join(parts, "\n\n")
}

func sessionPreviewForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if meta, ok, err := agent.LoadBranchMeta(path); err == nil && ok {
		if preview := strings.TrimSpace(meta.Preview); preview != "" {
			return preview
		}
	}
	preview, ok, err := agent.LoadSessionPreviewFromDisplayIndex(path)
	if err != nil || !ok {
		return ""
	}
	return preview
}

func topicSessionPreview(sessions []sessioncatalog.SessionRecord, path string) string {
	for _, session := range sessions {
		if sessionRuntimeKey(session.Path) == sessionRuntimeKey(path) {
			return strings.TrimSpace(session.Preview)
		}
	}
	return ""
}
