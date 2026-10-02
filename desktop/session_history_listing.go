package main

import (
	"reasonix/internal/config"
	"reasonix/internal/sessioncatalog"
	"sort"
)

// ListSessions returns the saved sessions newest-first for the history panel,
// marking the one the current conversation is writing to and attaching any
// user-chosen titles.
func (a *App) ListSessions() []SessionMeta {
	dir := a.activeSessionDir()
	active := a.activeSessionPath(dir)
	if tab := a.activeTab(); tab != nil {
		active = tab.currentSessionIdentity()
	}
	return a.listSessionsFromDir(dir, active)
}

// ListSessionsForTab returns sessions from the directory owned by tabID. Task
// Monitor uses this stable target after asynchronous control lookups so a tab
// switch cannot redirect the eventual session lookup to another workspace.
func (a *App) ListSessionsForTab(tabID string) []SessionMeta {
	target, err := a.taskMonitorTargetForTab(tabID)
	if err != nil {
		return []SessionMeta{}
	}
	active := target.sessionPath
	if tab := a.tabByID(tabID); tab != nil {
		active = tab.currentSessionIdentity()
	}
	return a.listSessionsFromDir(target.sessionDir, active)
}

func (a *App) listSessionsFromDir(dir, active string) []SessionMeta {
	v3 := a.listCanonicalSessionsFromDir(dir, active)
	state, stateErr := a.workspaceRegistry().Load(a.bootContext())
	if stateErr != nil {
		return v3
	}
	scope, root := "", ""
	if sameDesktopPath(dir, config.SessionDir()) || sameDesktopPath(dir, desktopSessionDir(globalWorkspaceRoot())) {
		scope = "global"
	} else {
		for _, target := range a.sessionCatalogTargets() {
			if sameDesktopPath(dir, target.Path) {
				scope, root = target.Scope, target.WorkspaceRoot
				break
			}
		}
	}
	historical := a.historicalCanonicalTopics(scope, root, state)
	saved, _ := readHistoricalSidecar()
	applyHistoricalPresentations(historical, saved)
	for _, node := range historical {
		v3 = append(v3, SessionMeta{Source: node.Source, Historical: true, PreparationStatus: node.PreparationStatus,
			Path: node.SessionPath, Title: node.Label, TopicID: node.TopicID, Scope: scope, WorkspaceRoot: root,
			Preview: node.Preview, Turns: node.Turns, TurnsState: node.TurnsState,
			CreatedAt: node.CreatedAt, LastActivityAt: node.LastActivityAt, ModTime: node.LastActivityAt})
	}
	adopted := adoptedSourceRows(state)
	catalog := a.sessionCatalog.Load()
	if catalog == nil {
		return v3
	}
	target := sessioncatalog.DirectoryTarget{Path: dir, Scope: "global"}
	for _, candidate := range a.sessionCatalogTargets() {
		if sameProjectRoot(candidate.Path, dir) {
			target = candidate
			break
		}
	}
	records, err := listCatalogSessionsForDirectory(a.bootContext(), catalog, target, dir)
	if err != nil {
		return v3
	}
	open := a.openSessionPaths(dir)
	channelRoutes := channelSessionRoutesForDir(dir)
	out := make([]SessionMeta, 0, len(records)+len(v3))
	out = append(out, v3...)
	for _, record := range records {
		if adopted[sessionRuntimeKey(record.Path)] {
			continue
		}
		_, isOpen := open[record.Path]
		meta := sessionMetaFromCatalog(record, record.Path == active, isOpen)
		if route, ok := channelRoutes[sessionRuntimeKey(record.Path)]; ok {
			applyChannelSessionRoute(&meta, route)
		}
		for _, row := range expandSessionSourceRows(ProjectNode{SessionPath: record.Path}) {
			if row.Source == nil {
				out = append(out, meta)
				continue
			}
			if adopted[projectNodeSessionKey(row)] {
				continue
			}
			headMeta := meta
			headMeta.Source = row.Source
			headMeta.Historical, headMeta.HistoricalBranch = true, row.HistoricalBranch
			headMeta.PreparationStatus = a.historicalPreparationStatus(row.Source.SourceKey)
			if presentation := saved.Presentations[row.Source.SourceKey]; presentation.Title != "" {
				headMeta.Title = presentation.Title
			}
			headMeta.Turns, headMeta.Preview = row.Turns, row.Preview
			if row.LastActivityAt > 0 {
				headMeta.LastActivityAt, headMeta.ModTime = row.LastActivityAt, row.LastActivityAt
			}
			if row.CreatedAt > 0 {
				headMeta.CreatedAt = row.CreatedAt
			}
			headMeta.Current = sessionRuntimeKey(headMeta.Path) == sessionRuntimeKey(active)
			out = append(out, headMeta)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastActivityAt > out[j].LastActivityAt })
	return out
}
