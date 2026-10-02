package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"reasonix/internal/config"
	"reasonix/internal/session"
)

const embeddedBotSessionPrefix = "bot-session:local:"

// Embedded bot conversations keep their existing project-local store. Resolve
// the sidebar's stable session reference there before creating a surface; a
// forged reference must not open a session belonging to another workspace.
func (a *App) embeddedBotSessionPath(scope, workspaceRoot, route string) (string, error) {
	id := strings.TrimPrefix(route, embeddedBotSessionPrefix)
	if err := session.ValidateSessionID(id); err != nil {
		return "", err
	}
	root := globalTabWorkspaceRoot()
	if scope == "project" {
		root = normalizeProjectRoot(workspaceRoot)
		if root == "" {
			return "", fmt.Errorf("workspaceRoot is required")
		}
	}
	storeRoot := session.RootForLegacyDir(config.ProjectSessionDir(root))
	service, err := a.historicalSessionService(storeRoot)
	if err != nil {
		return "", err
	}
	info, err := service.Query().Stat(a.bootContext(), session.SessionRef{HostID: localDesktopHostID, SessionID: id})
	if err != nil {
		return "", err
	}
	same, err := sameDesktopPathStrict(info.CWD, root)
	if err != nil {
		return "", err
	}
	if !same {
		return "", errSessionWorkspaceConflict
	}
	path := filepath.Join(storeRoot, id)
	if !nativeStoredDirectory(path) {
		return "", session.ErrSessionNotFound
	}
	return path, nil
}
