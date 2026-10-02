package main

import (
	"context"
	"os"
	"path/filepath"
	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/session"
	"strings"
)

// nativeSessionSelection belongs to the durable source, including detached views.
type nativeSessionSelection struct {
	SessionHeadID string `json:"sessionHeadId,omitempty"`
}

// A historical directory (or its retained JSONL alias) is represented by a
// bound runtime whose SessionPath is empty. Compare both store and identity;
// comparing only the path would rebuild that runtime on every admission.
func (a *App) controllerMatchesSessionPath(ctrl control.SessionAPI, path string) bool {
	if path == "" || sessionRuntimeKey(ctrl.SessionPath()) == sessionRuntimeKey(path) {
		return true
	}
	service, runtime, bound := exclusiveSessionBinding(ctrl)
	if !bound {
		return false
	}
	root, id := desktopSessionRoot(filepath.Dir(path)), agent.BranchID(path)
	if nativeStoredDirectory(path) {
		root, id = filepath.Dir(path), filepath.Base(path)
	}
	expected, err := a.historicalSessionService(root)
	return err == nil && service == expected && runtime.Ref().SessionID == id
}

func nativeStoredDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir() && hasHistoricalSessionArtifacts(path)
}

func (a *App) sessionServiceForSource(sessionDir, sessionID, path, storedDir string) (*session.Service, error) {
	if storedDir != "" {
		return a.historicalSessionService(filepath.Dir(storedDir))
	}
	if sessionID == "" && path != "" {
		return a.historicalSessionService(desktopSessionRoot(filepath.Dir(path)))
	}
	return a.desktopSessionService(sessionDir), nil
}

func (a *App) bindNativeDirectoryForTab(buildCtx context.Context, tab *WorkspaceTab, ctrl control.SessionAPI, storedLegacyDir, rootKey string, buildGeneration uint64) bool {
	identity := ctrl.(control.IdentityLifecycle)
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: filepath.Base(storedLegacyDir)}
	if _, bindErr := identity.OpenSession(buildCtx, ref); bindErr != nil {
		a.recordTabStartupFailure(tab, buildGeneration, a.ctx, friendlySessionLoadError(bindErr))
		ctrl.Close()
		a.releaseSharedHost(rootKey)
		return false
	}
	a.mu.Lock()
	if a.tabBuildSupersededLocked(tab, buildGeneration) {
		a.mu.Unlock()
		a.abandonSupersededBuild(tab, ctrl, rootKey, "")
		return false
	}
	tab.SessionID, tab.SessionPath = "", storedLegacyDir
	a.mu.Unlock()
	tab.replaceTelemetry(loadTelemetry(storedLegacyDir+".telemetry.json"), sessionRuntimeKey(storedLegacyDir))
	return true
}

func (a *App) validateNativeHeadLocked(path, headID string) error {
	if owner := a.liveRuntimeTabMatchingLocked(nil, path); owner != nil && owner.SessionHeadID != headID {
		return newSessionOperationError("busy", "Close the open head before opening another head of this historical session.")
	}
	return nil
}

func (a *App) nativeStartupSource(tab *WorkspaceTab, root, tabScope, tabWorkspaceRoot, tabTopicID, tabSessionPath string) (string, string, string) {
	sessionDir := desktopSessionDir(root)
	if tabScope == "global" {
		sessionDir = desktopSessionDir(globalWorkspaceRoot())
	}
	topicID := strings.TrimSpace(tabTopicID)
	pinnedPath, hasPinnedPath := pinnedTabSessionPathForBuild(tabScope, tabWorkspaceRoot, sessionDir, tabSessionPath)
	if hasPinnedPath && agent.IsCleanupPending(pinnedPath) {
		// Boot reconciliation may finish the pending deletion before the later
		// resume step. Clear the local candidate now so the disappeared path is
		// not mistaken for a deliberate empty placeholder afterward.
		hasPinnedPath = false
		pinnedPath = ""
	}
	catalogTopicPath := ""
	if hasPinnedPath {
		// A restored tab's exact path is already known state, not history
		// discovery. Keep legacy-directory and empty placeholder paths usable
		// while the catalog is still opening or rebuilding.
		sessionDir = filepath.Dir(pinnedPath)
	} else {
		catalogTopicPath = a.catalogSessionPathForTopic(tabScope, tabWorkspaceRoot, topicID)
	}
	if !hasPinnedPath && catalogTopicPath != "" {
		sessionDir = filepath.Dir(catalogTopicPath)
	}
	startupSessionPath := ""
	if hasPinnedPath {
		if !agent.IsCleanupPending(pinnedPath) {
			startupSessionPath = pinnedPath
		}
	} else if catalogTopicPath != "" {
		startupSessionPath = catalogTopicPath
	}
	if nativeStoredDirectory(tabSessionPath) {
		startupSessionPath = tabSessionPath
		sessionDir = filepath.Dir(tabSessionPath)
	}
	prepareStartupPinnedContext(tab, startupSessionPath, tabSessionPath)
	storedLegacyDir := ""
	if nativeStoredDirectory(startupSessionPath) {
		storedLegacyDir = startupSessionPath
	}
	return sessionDir, startupSessionPath, storedLegacyDir
}
