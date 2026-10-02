package main

import (
	"errors"
	"reasonix/internal/session"
	"strings"
)

// DeleteSession is the legacy archive RPC. Canonical content and legacy
// originals remain in place; the lifecycle owner rejects active runtimes.
func (a *App) DeleteSession(path string) error {
	_, err := a.archiveSessionPathWithOperation(path, "archive-"+strings.TrimPrefix(newTabID(), "tab_"))
	return friendlySessionFileError(err)
}

func (a *App) archiveSessionPathWithOperation(path, operationID string) (SessionTarget, error) {
	target, err := a.resolveSessionMutationTarget(sessionTargetSelector{SessionPath: path})
	if err != nil {
		return SessionTarget{}, err
	}
	if target.SessionRef.SessionID != "" {
		return a.archiveCanonicalSessionWithOperation(target.SessionRef, operationID)
	}
	a.cancelAISessionTitle(target.key())
	_, valid, err := a.sessionDirForPath(path)
	if err != nil {
		return SessionTarget{}, err
	}
	ref, adopted, err := a.legacyCanonicalRef(a.bootContext(), valid)
	if err != nil {
		return SessionTarget{}, err
	}
	if adopted {
		return a.archiveCanonicalSessionWithOperation(ref, operationID)
	}
	release, ok := a.tryLockRuntimeMutation("archive historical session")
	if !ok {
		return SessionTarget{}, errTopicArchiveBusy
	}
	ref, dependency, err := a.stageArchiveSource(a.bootContext(), valid)
	if err == nil {
		dependencies := []string{}
		if dependency != "" {
			dependencies = append(dependencies, dependency)
		}
		err = a.archiveSessionRefsWithOperation([]session.SessionRef{ref}, operationID, dependencies...)
	}
	release()
	if err == nil {
		a.emitProjectTreeChanged()
		archived, resolveErr := a.resolveCanonicalSessionTargetState(ref, "", true)
		if resolveErr != nil {
			archived = SessionTarget{SessionRef: ref}
		}
		a.emitSessionTargetChange("session_archived", SessionTargetChangeEvent{
			TargetKey: target.key(), OperationID: operationID,
			LifecycleGeneration: archived.LifecycleGeneration, WorkspaceID: archived.WorkspaceID,
		})
		return archived, nil
	}
	return SessionTarget{}, err
}

// PurgeTrashedSession resolves a legacy trash identity and removes only its
// canonical application session, retaining upgrade originals.
func (a *App) PurgeTrashedSession(path string) error {
	if _, err := a.trashedSessionDir(path); err != nil {
		return err
	}
	if !explicitlyDeletedLegacyEntry(path) {
		return errors.New("historical recovery entries cannot be permanently cleared")
	}
	// Legacy public RPCs retain upgrade originals too. Resolve/import the
	// archived application identity before delegating to canonical purge.
	if err := a.discoverHistoricalTrash(a.bootContext()); err != nil {
		return err
	}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return err
	}
	mapping, adopted, err := state.ResolveSource(desktopSourceKey(path, ""))
	if err != nil {
		return err
	}
	if adopted {
		return a.PurgeCanonicalSession(session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID})
	}
	return errors.New("historical session has no verified canonical identity")
}
