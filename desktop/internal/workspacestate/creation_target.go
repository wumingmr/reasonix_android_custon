package workspacestate

import (
	"context"
	"errors"
	"os"
	"strings"

	"reasonix/internal/pathidentity"
)

var (
	ErrCreationWorkspaceUnavailable = errors.New("creation workspace is unavailable")
	ErrCreationWorkspaceChanged     = errors.New("creation workspace names a different directory")
	ErrCreationSessionInactive      = errors.New("creation session was archived or removed")
)

// ResolveCreationWorkspace reconciles a journal's workspace ID without changing
// its operation/session identity or registering a removed workspace.
func ResolveCreationWorkspace(state State, workspaceID, root string) (Workspace, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return Workspace{}, ErrCreationWorkspaceUnavailable
	}
	if previous, ok := state.Workspaces[workspaceID]; ok {
		same, err := pathidentity.Same(previous.Root, root, pathidentity.Options{FollowLeaf: true})
		if err != nil {
			return Workspace{}, ErrCreationWorkspaceUnavailable
		}
		if !same {
			return Workspace{}, ErrCreationWorkspaceChanged
		}
	}
	id, ok, err := ResolveWorkspaceID(state, root)
	if err != nil {
		return Workspace{}, ErrCreationWorkspaceUnavailable
	}
	if !ok {
		return Workspace{}, ErrWorkspaceNotFound
	}
	return state.Workspaces[id], nil
}

func resolveCreationWorkspace(state State, workspaceID, root, sessionID string) (Workspace, error) {
	if lifecycle := state.SessionStates[sessionID].Lifecycle; lifecycle == Archived || lifecycle == Deleted {
		return Workspace{}, ErrCreationSessionInactive
	}
	return ResolveCreationWorkspace(state, workspaceID, root)
}

// These operations resolve under the registry transaction lock: another
// process may merge workspace IDs between reservation and publication.
func (s *Store) BeginCreateAtRoot(ctx context.Context, pending PendingCreate, root string) (Workspace, error) {
	if strings.TrimSpace(root) == "" {
		return Workspace{}, ErrCreationWorkspaceUnavailable
	}
	return s.beginCreate(ctx, pending, root)
}

func (s *Store) AttachCreatedSessionAtRoot(ctx context.Context, operationID, workspaceID, sessionID, root string) error {
	return s.attachSession(ctx, operationID, workspaceID, sessionID, "", "", nil, root)
}
