package workspacestate

import (
	"context"
	"errors"
	"strings"
	"time"
)

func (s *Store) BeginCreate(ctx context.Context, pending PendingCreate) error {
	_, err := s.beginCreate(ctx, pending, "")
	return err
}

func (s *Store) beginCreate(ctx context.Context, pending PendingCreate, root string) (Workspace, error) {
	var workspace Workspace
	pending.OperationID = strings.TrimSpace(pending.OperationID)
	pending.WorkspaceID = strings.TrimSpace(pending.WorkspaceID)
	pending.SessionID = strings.TrimSpace(pending.SessionID)
	if pending.OperationID == "" || pending.WorkspaceID == "" || pending.SessionID == "" {
		return workspace, errors.New("pending create requires operation, workspace, and session ids")
	}
	err := s.mutate(ctx, func(state *State) error {
		if root != "" {
			var err error
			workspace, err = resolveCreationWorkspace(*state, pending.WorkspaceID, root, pending.SessionID)
			if err != nil {
				return err
			}
			pending.WorkspaceID = workspace.ID
		}
		if _, ok := state.Workspaces[pending.WorkspaceID]; !ok {
			return ErrWorkspaceNotFound
		}
		if state.SessionStates[pending.SessionID].Lifecycle == Deleted {
			return ErrMutationConflict
		}
		if current, ok := state.PendingCreates[pending.SessionID]; ok {
			if current.OperationID == pending.OperationID && current.WorkspaceID == pending.WorkspaceID {
				return nil
			}
			return ErrMutationConflict
		}
		if owner, ok := sessionOwner(*state, pending.SessionID); ok && owner != pending.WorkspaceID {
			return ErrMutationConflict
		}
		if pending.CreatedAt.IsZero() {
			pending.CreatedAt = time.Now().UTC()
		}
		state.PendingCreates[pending.SessionID] = pending
		return nil
	})
	return workspace, err
}

func (s *Store) AttachSession(ctx context.Context, operationID, workspaceID, sessionID, beforeSessionID string) error {
	return s.attachSession(ctx, operationID, workspaceID, sessionID, beforeSessionID, "", nil)
}

// AttachSessionFromSourceIfUnchanged publishes a derived child only while its
// resolved source is still active, in the same workspace, and at the same
// lifecycle generation.
func (s *Store) AttachSessionFromSourceIfUnchanged(
	ctx context.Context,
	operationID, workspaceID, sessionID, beforeSessionID, sourceSessionID string,
	sourceGeneration uint64,
) error {
	return s.attachSession(ctx, operationID, workspaceID, sessionID, beforeSessionID, sourceSessionID, &sourceGeneration)
}

func (s *Store) attachSession(
	ctx context.Context,
	operationID, workspaceID, sessionID, beforeSessionID, sourceSessionID string,
	sourceGeneration *uint64,
	creationRoot ...string,
) error {
	operationID, workspaceID, sessionID = strings.TrimSpace(operationID), strings.TrimSpace(workspaceID), strings.TrimSpace(sessionID)
	if workspaceID == "" || sessionID == "" {
		return errors.New("attach requires workspace and session ids")
	}
	return s.mutate(ctx, func(state *State) error {
		if len(creationRoot) > 0 {
			workspace, err := resolveCreationWorkspace(*state, workspaceID, creationRoot[0], sessionID)
			if err != nil {
				return err
			}
			workspaceID = workspace.ID
		}
		if sourceGeneration != nil {
			sourceSessionID = strings.TrimSpace(sourceSessionID)
			sourceState := state.SessionStates[sourceSessionID]
			sourceOwner, owned := sessionOwner(*state, sourceSessionID)
			if sourceSessionID == "" || !owned || sourceOwner != workspaceID ||
				sourceState.Lifecycle != Active || sourceState.Generation != *sourceGeneration {
				return ErrMutationConflict
			}
		}
		if state.SessionStates[sessionID].Lifecycle == Deleted {
			return ErrMutationConflict
		}
		workspace, ok := state.Workspaces[workspaceID]
		if !ok {
			return ErrWorkspaceNotFound
		}
		if owner, owned := sessionOwner(*state, sessionID); owned {
			if owner != workspaceID {
				return ErrMutationConflict
			}
			delete(state.PendingCreates, sessionID)
			return nil
		}
		if operationID != "" {
			pending, ok := state.PendingCreates[sessionID]
			if !ok || pending.OperationID != operationID || pending.WorkspaceID != workspaceID {
				return ErrMutationConflict
			}
		}
		workspace.SessionIDs = insertBefore(workspace.SessionIDs, sessionID, beforeSessionID)
		if sourceSessionID == "" {
			sourceSessionID = state.PendingCreates[sessionID].ParentSessionID
		}
		attachOrganizationSession(&workspace, sessionID, sourceSessionID)
		mirrorOrganizationOrder(&workspace)
		if pending, ok := state.PendingCreates[sessionID]; ok && pending.Presentation != nil {
			state.Presentation[sessionID] = *pending.Presentation
		}
		workspace.UpdatedAt = time.Now().UTC()
		state.Workspaces[workspaceID] = workspace
		delete(state.PendingCreates, sessionID)
		return nil
	})
}
