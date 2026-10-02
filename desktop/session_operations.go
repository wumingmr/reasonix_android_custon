package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/agent"
	"reasonix/internal/session"
)

// SessionMutationResult is the stable response for an explicitly targeted
// persistent session operation. Versions are strings at the RPC boundary so
// JavaScript never truncates durable 64-bit sequence identities.
type SessionMutationResult struct {
	Outcome             string   `json:"outcome,omitempty"`
	TargetKey           string   `json:"targetKey"`
	OperationID         string   `json:"operationId"`
	Committed           bool     `json:"committed"`
	Title               string   `json:"title,omitempty"`
	TitleVersion        string   `json:"titleVersion,omitempty"`
	LifecycleGeneration uint64   `json:"lifecycleGeneration"`
	ProjectionPending   bool     `json:"projectionPending,omitempty"`
	IdentityAliases     []string `json:"identityAliases,omitempty"`
}

// SessionCreationResult reports a durable child created from an explicit
// source without implying that Desktop opened or selected it.
type SessionCreationResult struct {
	Ref               session.SessionRef `json:"ref"`
	OperationID       string             `json:"operationId"`
	Committed         bool               `json:"committed"`
	ProjectionPending bool               `json:"projectionPending,omitempty"`
}

// SessionTargetChangeEvent is an incremental projection hint. Durable storage
// remains authoritative; consumers that miss or cannot order these events
// re-read only the named target.
type SessionTargetChangeEvent struct {
	TargetKey           string `json:"targetKey"`
	OperationID         string `json:"operationId,omitempty"`
	LifecycleGeneration uint64 `json:"lifecycleGeneration"`
	Title               string `json:"title,omitempty"`
	WorkspaceID         string `json:"workspaceId,omitempty"`
}

func (a *App) emitSessionTargetChange(name string, event SessionTargetChangeEvent) {
	a.emitRuntimeEvent(name, event)
}

func sessionOperationErrorForTarget(err error, targetKey, operationID string) error {
	if err == nil {
		return nil
	}
	var operationErr *SessionOperationError
	if errors.As(err, &operationErr) {
		copy := *operationErr
		if copy.TargetKey == "" {
			copy.TargetKey = targetKey
		}
		if copy.OperationID == "" {
			copy.OperationID = operationID
		}
		return &copy
	}
	if historicalSourceBusyError(err) {
		return &SessionOperationError{Code: sessionOperationBusy, Message: "Another operation is using this session. Try again shortly.", TargetKey: targetKey, OperationID: operationID, Retryable: true}
	}
	switch {
	case errors.Is(err, workspacestate.ErrMutationConflict):
		return &SessionOperationError{
			Code: "target_changed", Message: "The session location or state changed. Reload it and try again.",
			TargetKey: targetKey, OperationID: operationID, Retryable: true,
		}
	case errors.Is(err, workspacestate.ErrSessionNotFound), errors.Is(err, session.ErrSessionNotFound):
		return &SessionOperationError{
			Code: sessionOperationTargetNotFound, Message: "The session no longer exists or has been removed.",
			TargetKey: targetKey, OperationID: operationID,
		}
	case errors.Is(err, session.ErrDamagedStore):
		slog.Warn("desktop: session store is damaged", "target", targetKey, "operation", operationID, "err", err)
		return &SessionOperationError{
			Code: sessionOperationDamaged, Message: "This session's saved file is damaged and cannot be read.",
			TargetKey: targetKey, OperationID: operationID,
		}
	case errors.Is(err, errTopicArchiveBusy), errors.Is(err, errTopicHasActiveWork), errors.Is(err, agent.ErrSessionLeaseHeld):
		return &SessionOperationError{
			Code: sessionOperationBusy, Message: "Another operation is using this session. Try again shortly.",
			TargetKey: targetKey, OperationID: operationID, Retryable: true,
		}
	}
	// The host log is the only place that still carries the cause.
	slog.Warn("desktop: unclassified session operation failure", "target", targetKey, "operation", operationID, "err", err)
	// RPC messages are user-visible. Do not pass through paths, lease holder
	// details, provider bodies, or credential-adjacent diagnostics from an
	// unclassified lower-level error.
	return &SessionOperationError{
		Code: sessionOperationFailed, Message: "Unable to complete this session operation.",
		TargetKey: targetKey, OperationID: operationID,
	}
}

// RenameSessionTarget performs a manual persistent rename without opening or
// selecting the target session.
func (a *App) RenameSessionTarget(selector SessionSelector, title string) (SessionMutationResult, error) {
	target, err := a.resolveSessionTarget(selector)
	if err != nil {
		return SessionMutationResult{}, err
	}
	key := target.key()
	operationID := "title-manual-" + strings.TrimPrefix(newTabID(), "tab_")
	a.cancelAISessionTitle(key)
	title = strings.TrimSpace(title)
	if target.SessionRef.SessionID != "" {
		err = a.renameCanonicalSessionTarget(target, title)
		if err == nil {
			info, statErr := a.desktopSessionService("").Query().Stat(a.bootContext(), target.SessionRef)
			if statErr != nil {
				result := SessionMutationResult{
					TargetKey: key, OperationID: operationID, Committed: true, Title: title,
					LifecycleGeneration: target.LifecycleGeneration, ProjectionPending: true,
				}
				a.emitSessionTargetChange("session_metadata_changed", SessionTargetChangeEvent{
					TargetKey: key, OperationID: operationID, LifecycleGeneration: target.LifecycleGeneration,
					Title: title, WorkspaceID: target.WorkspaceID,
				})
				return result, nil
			}
			result := SessionMutationResult{
				TargetKey: key, OperationID: operationID, Committed: true, Title: title,
				TitleVersion:        titleSequenceVersion(info.TitleSequence),
				LifecycleGeneration: target.LifecycleGeneration,
			}
			a.emitSessionTargetChange("session_metadata_changed", SessionTargetChangeEvent{
				TargetKey: key, OperationID: operationID, LifecycleGeneration: target.LifecycleGeneration, Title: title,
				WorkspaceID: target.WorkspaceID,
			})
			return result, nil
		}
	} else if target.SessionPath != "" {
		if target.Source != nil {
			err = a.saveHistoricalSourcePresentation(target.Source.SourceKey, func(presentation *historicalSourcePresentation) {
				presentation.Title = title
			})
		}
		if err != nil {
			return SessionMutationResult{}, sessionOperationErrorForTarget(err, key, operationID)
		}
		if info, statErr := os.Stat(target.SessionPath); target.Source != nil && statErr == nil && info.IsDir() {
			a.emitSessionTargetChange("session_metadata_changed", SessionTargetChangeEvent{TargetKey: key, OperationID: operationID, Title: title})
			a.emitProjectTreeMetadataChanged()
			return SessionMutationResult{TargetKey: key, OperationID: operationID, Committed: true, Title: title}, nil
		}
		err = a.RenameSession(target.SessionPath, title)
		if err == nil {
			_, revision, revisionErr := agent.SessionTitleSnapshot(target.SessionPath)
			result := SessionMutationResult{
				TargetKey: key, OperationID: operationID, Committed: true, Title: title,
				TitleVersion: revision, LifecycleGeneration: target.LifecycleGeneration,
				ProjectionPending: revisionErr != nil,
			}
			a.emitSessionTargetChange("session_metadata_changed", SessionTargetChangeEvent{
				TargetKey: key, OperationID: operationID, LifecycleGeneration: target.LifecycleGeneration, Title: title,
			})
			return result, nil
		}
	} else if target.TopicID != "" {
		err = a.RenameTopic(target.TopicID, title)
		if err == nil {
			result := SessionMutationResult{
				TargetKey: key, OperationID: operationID, Committed: true, Title: title,
				LifecycleGeneration: target.LifecycleGeneration,
			}
			a.emitSessionTargetChange("session_metadata_changed", SessionTargetChangeEvent{
				TargetKey: key, OperationID: operationID, LifecycleGeneration: target.LifecycleGeneration, Title: title,
			})
			return result, nil
		}
	} else {
		err = newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	return SessionMutationResult{}, sessionOperationErrorForTarget(err, key, operationID)
}

func titleSequenceVersion(sequence uint64) string {
	if sequence == 0 {
		return ""
	}
	return "event:" + strconv.FormatUint(sequence, 10)
}

// ArchiveSessionTarget archives one explicit durable target. It does not select
// the target or create a conversation controller.
func (a *App) ArchiveSessionTarget(selector SessionSelector) (SessionMutationResult, error) {
	if selector.Ref == nil && selector.Source != nil {
		return a.archiveHistoricalSource(selector)
	}
	if resolved, err := a.resolveSessionTarget(selector); err == nil && resolved.Source != nil {
		return a.archiveHistoricalSource(SessionSelector{Source: resolved.Source, TopicID: resolved.TopicID})
	}
	target, err := a.resolveSessionMutationTarget(selector)
	if err != nil {
		return SessionMutationResult{}, err
	}
	key := target.key()
	operationID := "archive-" + strings.TrimPrefix(newTabID(), "tab_")
	var archived SessionTarget
	if target.SessionRef.SessionID != "" {
		archived, err = a.archiveCanonicalSessionWithOperation(target.SessionRef, operationID)
	} else if target.SessionPath != "" {
		archived, err = a.archiveSessionPathWithOperation(target.SessionPath, operationID)
	} else {
		err = newSessionOperationError(sessionOperationNoMessages, "This empty session has no durable content to archive.")
	}
	if err != nil {
		return SessionMutationResult{}, sessionOperationErrorForTarget(err, key, operationID)
	}
	return SessionMutationResult{
		TargetKey: key, OperationID: operationID, Committed: true,
		Outcome:             "archived",
		LifecycleGeneration: archived.LifecycleGeneration,
		ProjectionPending:   archived.LifecycleGeneration == 0,
		IdentityAliases:     a.sessionTargetIdentityAliases(target),
	}, nil
}

func (a *App) sessionTargetIdentityAliases(target SessionTarget) []string {
	aliases := []string{projectNodeSessionKey(ProjectNode{SessionPath: target.SessionPath})}
	if target.SessionRef.SessionID != "" {
		aliases = append(aliases, projectNodeSessionKey(ProjectNode{Session: &target.SessionRef}))
		if state, err := a.workspaceRegistry().Load(a.bootContext()); err == nil {
			aliases = append(aliases, sourceAliases(state, desktopWorkspaceOwnerID(state, target.Scope, target.WorkspaceRoot), target.SessionRef.SessionID)...)
		}
	}
	return aliases
}

// RestoreSessionTarget restores one explicit archived target. Canonical
// sessions use the workspace journal; pre-adoption legacy trash entries retain
// the existing guarded restore flow.
func (a *App) RestoreSessionTarget(selector SessionSelector) (SessionMutationResult, error) {
	operationID := "restore-" + strings.TrimPrefix(newTabID(), "tab_")
	target, err := a.resolveSessionTargetWithArchived(selector, true)
	if err == nil {
		key := target.key()
		if target.SessionRef.SessionID != "" {
			if target.Lifecycle != workspacestate.Archived {
				err = newSessionOperationError("target_changed", "This session is not archived.")
			} else {
				var restored SessionTarget
				restored, err = a.restoreCanonicalSessionWithOperation(target.SessionRef, operationID)
				if err == nil {
					return SessionMutationResult{
						TargetKey: key, OperationID: operationID, Committed: true,
						LifecycleGeneration: restored.LifecycleGeneration,
						ProjectionPending:   restored.LifecycleGeneration == 0,
					}, nil
				}
			}
		} else {
			err = newSessionOperationError("target_changed", "This session is not archived.")
		}
		return SessionMutationResult{}, sessionOperationErrorForTarget(err, key, operationID)
	}

	// A legacy trash path is intentionally outside the live-session resolver.
	// Honor selector priority and try this only when sessionPath is the explicit
	// highest-priority field.
	if selector.Ref != nil || strings.TrimSpace(selector.SessionPath) == "" {
		return SessionMutationResult{}, sessionOperationErrorForTarget(err, "", operationID)
	}
	trashPath := strings.TrimSpace(selector.SessionPath)
	dir, trashErr := a.trashedSessionDir(trashPath)
	if trashErr != nil {
		return SessionMutationResult{}, sessionOperationErrorForTarget(err, "", operationID)
	}
	_, keyName, _, trashErr := validateTrashedSessionPath(dir, trashPath)
	if trashErr != nil {
		return SessionMutationResult{}, sessionOperationErrorForTarget(trashErr, "", operationID)
	}
	targetKey := (SessionTarget{SessionPath: trashPath}).key()
	a.cancelAISessionTitle(targetKey)
	if restoreErr := a.RestoreSession(trashPath); restoreErr != nil {
		return SessionMutationResult{}, sessionOperationErrorForTarget(restoreErr, targetKey, operationID)
	}
	livePath := filepath.Join(dir, keyName)
	restored, resolveErr := a.resolveSessionTarget(SessionSelector{SessionPath: livePath})
	if resolveErr != nil {
		restored = SessionTarget{SessionPath: livePath}
	}
	a.emitSessionTargetChange("session_restored", SessionTargetChangeEvent{
		TargetKey: targetKey, OperationID: operationID,
		LifecycleGeneration: restored.LifecycleGeneration, WorkspaceID: restored.WorkspaceID,
	})
	return SessionMutationResult{
		TargetKey: targetKey, OperationID: operationID, Committed: true,
		LifecycleGeneration: restored.LifecycleGeneration,
		ProjectionPending:   resolveErr != nil,
	}, nil
}

// MoveSessionTarget reorders one explicit session inside its owning workspace.
// A legacy source is first adopted through the existing migration journal so
// the move operates on a durable canonical identity rather than a path alias.
func (a *App) MoveSessionTarget(selector SessionSelector, workspaceID, beforeSessionID string) (SessionMutationResult, error) {
	target, err := a.resolveSessionMutationTarget(selector)
	if err != nil {
		return SessionMutationResult{}, err
	}
	key := target.key()
	operationID := "move-" + strings.TrimPrefix(newTabID(), "tab_")
	if target.SessionRef.SessionID == "" {
		scope, root := target.Scope, target.WorkspaceRoot
		if scope == "" {
			scope = "global"
		}
		adoptionWorkspace, ensureErr := a.ensureDesktopWorkspace(a.bootContext(), scope, root)
		if ensureErr != nil {
			return SessionMutationResult{}, sessionOperationErrorForTarget(ensureErr, key, operationID)
		}
		if strings.TrimSpace(workspaceID) == "" {
			workspaceID = adoptionWorkspace
		}
		if workspaceID != adoptionWorkspace {
			err = newSessionOperationError("target_changed", "The session cannot be moved outside its owning workspace.")
			return SessionMutationResult{}, sessionOperationErrorForTarget(err, key, operationID)
		}
		if migrateErr := a.migrateLegacySession(
			a.bootContext(),
			target.SessionPath,
			desktopMigrationSource{scope: scope, workspaceRoot: root},
			adoptionWorkspace,
		); migrateErr != nil {
			return SessionMutationResult{}, sessionOperationErrorForTarget(migrateErr, key, operationID)
		}
		adopted, adoptedErr := a.resolveSessionTarget(SessionSelector{SessionPath: target.SessionPath})
		if adoptedErr != nil || adopted.SessionRef.SessionID == "" {
			if adoptedErr == nil {
				adoptedErr = workspacestate.ErrMutationConflict
			}
			return SessionMutationResult{}, sessionOperationErrorForTarget(adoptedErr, key, operationID)
		}
		target = adopted
	}
	moved, err := a.moveWorkspaceSessionWithOperation(target.SessionRef, workspaceID, beforeSessionID, operationID)
	if err != nil {
		return SessionMutationResult{}, sessionOperationErrorForTarget(err, key, operationID)
	}
	return SessionMutationResult{
		TargetKey: key, OperationID: operationID, Committed: true,
		LifecycleGeneration: moved.LifecycleGeneration,
		ProjectionPending:   moved.LifecycleGeneration == 0,
	}, nil
}

// DeleteSessionTarget permanently removes one explicit archived canonical
// application session. Upgrade-source artifacts retain the existing tombstone
// policy and are never silently erased.
func (a *App) DeleteSessionTarget(selector SessionSelector) (SessionMutationResult, error) {
	target, err := a.resolveSessionTargetWithArchived(selector, true)
	operationID := "delete-" + strings.TrimPrefix(newTabID(), "tab_")
	if err != nil && selector.Ref != nil {
		target, err = a.resolveCanonicalPurgeTarget(*selector.Ref)
	}
	if err != nil {
		return SessionMutationResult{}, sessionOperationErrorForTarget(err, "", operationID)
	}
	key := target.key()
	if target.SessionRef.SessionID == "" {
		err = newSessionOperationError("unsupported", "This historical recovery source cannot be permanently deleted.")
		return SessionMutationResult{}, sessionOperationErrorForTarget(err, key, operationID)
	}
	if target.Lifecycle != workspacestate.Archived && target.Lifecycle != workspacestate.Deleted {
		err = newSessionOperationError("archived", "Archive this session before deleting it.")
		return SessionMutationResult{}, sessionOperationErrorForTarget(err, key, operationID)
	}
	deleted, err := a.purgeCanonicalSessionWithOperation(target.SessionRef, operationID)
	if err != nil {
		return SessionMutationResult{}, sessionOperationErrorForTarget(err, key, operationID)
	}
	return SessionMutationResult{
		TargetKey: key, OperationID: operationID, Committed: true,
		LifecycleGeneration: deleted.LifecycleGeneration,
	}, nil
}
