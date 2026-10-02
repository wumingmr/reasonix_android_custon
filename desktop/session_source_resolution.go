package main

import (
	"os"
	"path/filepath"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/session"
	"slices"
	"strings"
)

func (a *App) resolveSourceSessionTarget(selector SessionSelector, allowArchived bool) (SessionTarget, error) {
	source := selector.Source
	if source.HostID != "" && source.HostID != localDesktopHostID {
		return SessionTarget{}, newSessionOperationError("unsupported", "This source belongs to another host.")
	}
	if strings.TrimSpace(source.Path) == "" {
		return SessionTarget{}, newSessionOperationError("target_not_found", "The source no longer exists.")
	}
	key := desktopSourceKey(source.Path, source.HeadID)
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return SessionTarget{}, err
	}
	if source.SourceKey != "" && !slices.Contains(state.SourceKeys(source.SourceKey), key) {
		return SessionTarget{}, newSessionOperationError("target_changed", "The source identity changed.")
	}
	mapping, ok, err := state.ResolveSource(key)
	if err != nil {
		return SessionTarget{}, err
	}
	if ok {
		return a.resolveCanonicalSessionTargetState(session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}, selector.TopicID, allowArchived)
	}
	if source.HeadID == "" {
		canonical, _ := a.desktopHistoricalRoots()
		for _, root := range canonical {
			if !sameDesktopPath(filepath.Dir(source.Path), root.root) {
				continue
			}
			info, statErr := os.Lstat(source.Path)
			pending := pendingHistoricalOperation(state, key)
			if statErr != nil && !(os.IsNotExist(statErr) && pending != nil) {
				return SessionTarget{}, newSessionOperationError("target_not_found", "The historical source is unavailable.")
			}
			if info != nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
				return SessionTarget{}, newSessionOperationError("target_not_found", "The historical source is not a session directory.")
			}
			copy := *source
			copy.HostID, copy.SourceKey = localDesktopHostID, key
			return SessionTarget{Source: &copy, SessionPath: source.Path, TopicID: selector.TopicID,
				Scope: root.scope, WorkspaceRoot: root.workspaceRoot}, nil
		}
	}
	if source.HeadID != "" {
		target, err := a.resolveHistoricalHeadTarget(*source, key)
		return recoverHistoricalRuntimeOwner(target), err
	}
	target, err := a.resolveLegacySessionTarget(source.Path, selector.TopicID, allowArchived)
	if err == nil && target.SessionRef.SessionID == "" {
		copy := *source
		copy.HostID, copy.SourceKey = localDesktopHostID, key
		target.Source = &copy
		target = recoverHistoricalRuntimeOwner(target)
	}
	return target, err
}

// Global legacy storage is independent of old project metadata. If that
// metadata names a removed project, recover its actual global storage owner.
// A source inside a project never gains authority to run in another project.
func recoverHistoricalRuntimeOwner(target SessionTarget) SessionTarget {
	if target.Scope != "project" || target.Source == nil {
		return target
	}
	if info, err := os.Stat(target.WorkspaceRoot); err == nil && info.IsDir() || err != nil && !os.IsNotExist(err) {
		return target
	}
	dir := filepath.Dir(target.Source.Path)
	if sameDesktopPath(dir, config.SessionDir()) || sameDesktopPath(dir, desktopSessionDir(globalWorkspaceRoot())) {
		target.Scope, target.WorkspaceRoot = "global", ""
	}
	return target
}

func (a *App) resolveHistoricalHeadTarget(source SessionSourceRef, key string) (SessionTarget, error) {
	dir, validated, err := a.sessionDirForPath(source.Path)
	if err != nil {
		return SessionTarget{}, err
	}
	if _, _, err = validateSessionPath(dir, validated); err != nil {
		return SessionTarget{}, err
	}
	heads, err := agent.ListSessionHeads(validated)
	if err != nil {
		return SessionTarget{}, err
	}
	found := false
	for _, head := range heads {
		if head.ID == source.HeadID && !head.Retired {
			found = true
		}
	}
	if !found {
		return SessionTarget{}, newSessionOperationError("target_not_found", "This historical head is no longer available.")
	}
	source.Path, source.HostID, source.SourceKey = validated, localDesktopHostID, key
	target := SessionTarget{Source: &source, SessionPath: validated, Scope: "global"}
	for _, project := range loadProjectsFile().Projects {
		if canonicalRuntimeRoot(dir) == canonicalRuntimeRoot(desktopSessionDir(project.Root)) {
			target.Scope, target.WorkspaceRoot = "project", project.Root
			break
		}
	}
	if meta, ok, err := agent.LoadBranchMeta(validated); err == nil && ok {
		target.TopicID = meta.TopicID
		if meta.Scope != "" {
			target.Scope, target.WorkspaceRoot = meta.Scope, meta.WorkspaceRoot
		}
	}
	return target, nil
}
